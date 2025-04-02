package metrics

import (
	"fmt"
	"net"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
)

// Metrics holds all prometheus metrics
type Metrics struct {
	// Custom registry
	Registry *prometheus.Registry

	// HTTP metrics
	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec

	// Faucet metrics
	TransfersTotal *prometheus.CounterVec
	TransferAmount *prometheus.HistogramVec

	// Rate limiting metrics
	RateLimitedTotal   prometheus.Counter
	CaptchaFailedTotal prometheus.Counter
	CaptchaSolvedTotal prometheus.Counter

	// Connection metrics
	ActiveConnections prometheus.Gauge

	// Faucet address
	faucetAddress string
}

// NewMetrics creates and registers all metrics, including collectors
func NewMetrics(namespace string, faucetAddress string, providerURL string, address common.Address) *Metrics {
	// Create a custom registry
	registry := prometheus.NewRegistry()

	m := &Metrics{
		Registry:      registry,
		faucetAddress: faucetAddress,

		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "http_requests_total",
				Help:      "Total number of HTTP requests processed, partitioned by status code and HTTP method.",
			},
			[]string{"code", "method"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "http_request_duration_seconds",
				Help:      "Duration of HTTP requests in seconds.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"handler", "method"},
		),
		TransfersTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "transfers_total",
				Help:      "Total number of ETH transfers processed, partitioned by status.",
			},
			[]string{"status", "address"},
		),
		TransferAmount: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "transfer_amount_ether",
				Help:      "Amount of ETH transferred in each successful transaction.",
				Buckets:   prometheus.LinearBuckets(0.1, 0.1, 10), // 0.1 to 1.0 ETH in 0.1 increments
			},
			[]string{"address"},
		),
		RateLimitedTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "rate_limited_total",
				Help:      "Total number of requests that were rate-limited.",
			},
		),
		CaptchaFailedTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "captcha_failed_total",
				Help:      "Total number of requests with failed captcha verification.",
			},
		),
		CaptchaSolvedTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "captchas_solved_total",
				Help:      "Total number of captchas successfully solved.",
			},
		),
		ActiveConnections: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "active_connections",
				Help:      "Current number of active connections.",
			},
		),
	}

	// Register metrics with our custom registry
	registry.MustRegister(m.RequestsTotal)
	registry.MustRegister(m.RequestDuration)
	registry.MustRegister(m.TransfersTotal)
	registry.MustRegister(m.TransferAmount)
	registry.MustRegister(m.RateLimitedTotal)
	registry.MustRegister(m.CaptchaFailedTotal)
	registry.MustRegister(m.CaptchaSolvedTotal)
	registry.MustRegister(m.ActiveConnections)

	registry.MustRegister(collectors.NewGoCollector())
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	balanceCollector, err := NewBalanceCollector(providerURL, address, faucetAddress)
	if err != nil {
		log.WithError(err).Error("Failed to create balance collector")
	} else {
		err = registry.Register(balanceCollector)
		if err != nil {
			log.WithError(err).Error("Failed to register balance collector")
		}
	}

	nonceCollector, err := NewNonceCollector(providerURL, address, faucetAddress)
	if err != nil {
		log.WithError(err).Error("Failed to create nonce collector")
	} else {
		err = registry.Register(nonceCollector)
		if err != nil {
			log.WithError(err).Error("Failed to register nonce collector")
		}
	}

	return m
}

// RegisterBalanceCollector registers a collector for faucet balance
func (m *Metrics) RegisterBalanceCollector(providerURL string, address common.Address) error {
	collector, err := NewBalanceCollector(providerURL, address, m.faucetAddress)
	if err != nil {
		return err
	}

	return m.Registry.Register(collector)
}

// RegisterNonceCollector registers a collector for faucet nonces
func (m *Metrics) RegisterNonceCollector(providerURL string, address common.Address) error {
	collector, err := NewNonceCollector(providerURL, address, m.faucetAddress)
	if err != nil {
		return err
	}

	return m.Registry.Register(collector)
}

// GetHandler returns an HTTP handler for metrics
func (m *Metrics) GetHandler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// StartServer starts a separate HTTP server for metrics
func (m *Metrics) StartServer(port int, path string) error {
	if port <= 0 {
		return nil
	}

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot start metrics server: %w", err)
	}
	listener.Close()

	router := http.NewServeMux()
	router.Handle(path, m.GetHandler())

	go func() {
		log.Infof("Starting metrics server on %s%s", addr, path)
		err := http.ListenAndServe(addr, router)
		if err != nil {
			log.Errorf("Failed to start metrics server: %v", err)
		}
	}()
	return nil
}

// RecordRequest records a request with the given status code and method
func (m *Metrics) RecordRequest(code string, method string) {
	m.RequestsTotal.WithLabelValues(code, method).Inc()
}

// ObserveRequestDuration observes the duration of a request
func (m *Metrics) ObserveRequestDuration(handler string, method string, duration float64) {
	m.RequestDuration.WithLabelValues(handler, method).Observe(duration)
}

// RecordTransfer records a transfer with the given status
func (m *Metrics) RecordTransfer(status string) {
	m.TransfersTotal.WithLabelValues(status, m.faucetAddress).Inc()
}

// ObserveTransferAmount observes the amount of a transfer
func (m *Metrics) ObserveTransferAmount(amount float64) {
	m.TransferAmount.WithLabelValues(m.faucetAddress).Observe(amount)
}

// IncrementRateLimited increments the rate limited counter
func (m *Metrics) IncrementRateLimited() {
	m.RateLimitedTotal.Inc()
}

// IncrementCaptchaFailed increments the captcha failed counter
func (m *Metrics) IncrementCaptchaFailed() {
	m.CaptchaFailedTotal.Inc()
}

// IncrementCaptchasSolved increments the captchas solved counter
func (m *Metrics) IncrementCaptchasSolved() {
	m.CaptchaSolvedTotal.Inc()
}

// IncrementActiveConnections increments the active connections gauge
func (m *Metrics) IncrementActiveConnections() {
	m.ActiveConnections.Inc()
}

// DecrementActiveConnections decrements the active connections gauge
func (m *Metrics) DecrementActiveConnections() {
	m.ActiveConnections.Dec()
}
