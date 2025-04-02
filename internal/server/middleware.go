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
			l.metrics.IncrementRateLimited()
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
			c.metrics.IncrementCaptchaFailed()
		}
		return
	}

	if c.metrics != nil {
		c.metrics.IncrementCaptchasSolved()
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

	if m.metrics != nil {
		m.metrics.IncrementActiveConnections()
		defer m.metrics.DecrementActiveConnections()
	}

	next.ServeHTTP(w, r)

	if m.metrics != nil {
		duration := time.Since(start).Seconds()
		statusCode := strconv.Itoa(w.(negroni.ResponseWriter).Status())

		m.metrics.RecordRequest(statusCode, r.Method)

		if r.URL.Path == "/api/claim" || r.URL.Path == "/api/info" || r.URL.Path == "/metrics" {
			m.metrics.ObserveRequestDuration(r.URL.Path, r.Method, duration)
		}
	}
}
