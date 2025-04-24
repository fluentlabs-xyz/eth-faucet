package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v2"
	"github.com/kataras/hcaptcha"
	"github.com/rs/cors"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/negroni/v3"

	"github.com/chainflag/eth-faucet/internal/metrics"
)

type Limiter struct {
	mutex      sync.Mutex
	cache      *ttlcache.Cache
	proxyCount int
	ttl        time.Duration
	metrics    *metrics.Metrics
}

func NewLimiter(proxyCount int, ttl time.Duration, metrics *metrics.Metrics) *Limiter {
	cache := ttlcache.NewCache()
	cache.SkipTTLExtensionOnHit(true)
	return &Limiter{
		cache:      cache,
		proxyCount: proxyCount,
		ttl:        ttl,
		metrics:    metrics,
	}
}

func (l *Limiter) ServeHTTP(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	address, err := readAddress(r)
	if err != nil {
		var mr *malformedRequest
		if errors.As(err, &mr) {
			renderJSON(w, claimResponse{Message: mr.message}, mr.status)
		} else {
			renderJSON(w, claimResponse{Message: http.StatusText(http.StatusInternalServerError)}, http.StatusInternalServerError)
		}
		return
	}

	if l.ttl <= 0 {
		next.ServeHTTP(w, r)
		return
	}

	clientIP := getClientIPFromRequest(l.proxyCount, r)
	l.mutex.Lock()
	if l.limitByKey(w, address) || l.limitByKey(w, clientIP) {
		l.mutex.Unlock()
		if l.metrics != nil {
			l.metrics.RecordFilteredRequest("rate_limit", r.URL.Path)
		}
		return
	}
	l.cache.SetWithTTL(address, true, l.ttl)
	l.cache.SetWithTTL(clientIP, true, l.ttl)
	l.mutex.Unlock()

	next.ServeHTTP(w, r)
	if w.(negroni.ResponseWriter).Status() != http.StatusOK {
		l.cache.Remove(address)
		l.cache.Remove(clientIP)
		return
	}
	log.WithFields(log.Fields{
		"address":  address,
		"clientIP": clientIP,
	}).Info("Maximum request limit has been reached")
}

func (l *Limiter) limitByKey(w http.ResponseWriter, key string) bool {
	if _, ttl, err := l.cache.GetWithTTL(key); err == nil {
		errMsg := fmt.Sprintf("You have exceeded the rate limit. Please wait %s before you try again", ttl.Round(time.Second))
		renderJSON(w, claimResponse{Message: errMsg}, http.StatusTooManyRequests)
		return true
	}
	return false
}

func getClientIPFromRequest(proxyCount int, r *http.Request) string {
	if proxyCount > 0 {
		xForwardedFor := r.Header.Get("X-Forwarded-For")
		if xForwardedFor != "" {
			xForwardedForParts := strings.Split(xForwardedFor, ",")
			// Avoid reading the user's forged request header by configuring the count of reverse proxies
			partIndex := len(xForwardedForParts) - proxyCount
			if partIndex < 0 {
				partIndex = 0
			}
			return strings.TrimSpace(xForwardedForParts[partIndex])
		}
	}

	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}
	return remoteIP
}

type Captcha struct {
	client  *hcaptcha.Client
	secret  string
	metrics *metrics.Metrics
}

func NewCaptcha(hcaptchaSiteKey, hcaptchaSecret string, metrics *metrics.Metrics) *Captcha {
	client := hcaptcha.New(hcaptchaSecret)
	client.SiteKey = hcaptchaSiteKey
	return &Captcha{
		client:  client,
		secret:  hcaptchaSecret,
		metrics: metrics,
	}
}

func (c *Captcha) ServeHTTP(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	if c.secret == "" {
		next.ServeHTTP(w, r)
		return
	}

	response := c.client.VerifyToken(r.Header.Get("h-captcha-response"))
	if !response.Success {
		renderJSON(w, claimResponse{Message: "Captcha verification failed, please try again"}, http.StatusTooManyRequests)
		if c.metrics != nil {
			c.metrics.RecordFilteredRequest("captcha", r.URL.Path)
		}
		return
	}

	next.ServeHTTP(w, r)
}

// RequestMetrics is a middleware that records Prometheus metrics for HTTP requests
type RequestMetrics struct {
	metrics *metrics.Metrics
}

// NewRequestMetrics creates a new metrics middleware
func NewRequestMetrics(metrics *metrics.Metrics) *RequestMetrics {
	return &RequestMetrics{
		metrics: metrics,
	}
}

// ServeHTTP implements the negroni.Handler interface
func (m *RequestMetrics) ServeHTTP(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	start := time.Now()

	next.ServeHTTP(w, r)

	if m.metrics != nil {
		duration := time.Since(start).Seconds()
		statusCode := strconv.Itoa(w.(negroni.ResponseWriter).Status())
		path := r.URL.Path

		m.metrics.RecordRequest(statusCode, r.Method, path)
		m.metrics.ObserveRequestDuration(path, r.Method, duration)
	}
}

// CorsMiddleware handles CORS headers and preflight requests
type CorsMiddleware struct {
	allowed string
	headers string
	methods string
	cors    *cors.Cors
}

// NewCorsMiddleware creates a new CORS middleware using rs/cors
func NewCorsMiddleware(allowed, headers, methods string) negroni.Handler {
	// Create a new CORS handler with rs/cors
	c := cors.New(cors.Options{
		// We'll handle the headers manually to match the original implementation
		OptionsPassthrough: true, // Let us handle OPTIONS requests manually
	})

	return &CorsMiddleware{
		allowed: allowed,
		headers: headers,
		methods: methods,
		cors:    c,
	}
}

// ServeHTTP implements the negroni.Handler interface
func (c *CorsMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	// Set CORS headers manually to match the original implementation
	origin := r.Header.Get("Origin")

	// Handle allowed origins
	if c.allowed == "*" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else if origin != "" {
		// Check if the origin is in the allowed list
		allowedOrigins := strings.Split(c.allowed, ",")
		for _, allowedOrigin := range allowedOrigins {
			if strings.TrimSpace(allowedOrigin) == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				break
			}
		}
	}

	// Set other CORS headers
	w.Header().Set("Access-Control-Allow-Methods", c.methods)
	w.Header().Set("Access-Control-Allow-Headers", c.headers)

	// Handle preflight OPTIONS requests
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	next.ServeHTTP(w, r)
}
