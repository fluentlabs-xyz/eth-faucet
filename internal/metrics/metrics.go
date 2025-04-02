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

	// Filtered requests metrics
	FilteredRequestsTotal *prometheus.CounterVec

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
				Help:      "Total number of HTTP requests processed, partitioned by status code, HTTP method, and path.",
			},
			[]string{"code", "method", "path"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "http_request_duration_seconds",
				Help:      "Duration of HTTP requests in seconds.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"path", "method"},
		),
		FilteredRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "filtered_requests_total",
				Help:      "Total number of requests filtered by middleware, partitioned by reason and path.",
			},
			[]string{"reason", "path"},
		),
	}

	// Register metrics with our custom registry
	registry.MustRegister(m.RequestsTotal)
	registry.MustRegister(m.RequestDuration)
	registry.MustRegister(m.FilteredRequestsTotal)

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

// RecordRequest records a request with the given status code, method, and path
func (m *Metrics) RecordRequest(code string, method string, path string) {
	m.RequestsTotal.WithLabelValues(code, method, path).Inc()
}

// ObserveRequestDuration observes the duration of a request
func (m *Metrics) ObserveRequestDuration(path string, method string, duration float64) {
	m.RequestDuration.WithLabelValues(path, method).Observe(duration)
}

// RecordFilteredRequest records a filtered request with the given reason and path
func (m *Metrics) RecordFilteredRequest(reason string, path string) {
	m.FilteredRequestsTotal.WithLabelValues(reason, path).Inc()
}

// Legacy methods for backward compatibility

// RecordTransfer is a legacy method that maps to RecordRequest
func (m *Metrics) RecordTransfer(status string) {
	// No-op as we're not tracking transfers separately anymore
}

// ObserveTransferAmount is a legacy method that is now a no-op
func (m *Metrics) ObserveTransferAmount(amount float64) {
	// No-op as we're not tracking transfer amounts anymore
}

// IncrementRateLimited is a legacy method that maps to RecordFilteredRequest
func (m *Metrics) IncrementRateLimited() {
	// No-op as we'll use RecordFilteredRequest directly
}

// IncrementCaptchaFailed is a legacy method that maps to RecordFilteredRequest
func (m *Metrics) IncrementCaptchaFailed() {
	// No-op as we'll use RecordFilteredRequest directly
}

// IncrementCaptchasSolved is a legacy method that is now a no-op
func (m *Metrics) IncrementCaptchasSolved() {
	// No-op as we're not tracking this separately anymore
}

// RecordFaucetRequest is a legacy method that is now a no-op
func (m *Metrics) RecordFaucetRequest(status string) {
	// No-op as we're not tracking this separately anymore
}

// IncrementActiveConnections is a legacy method that is now a no-op
func (m *Metrics) IncrementActiveConnections() {
	// No-op as we're not tracking active connections anymore
}

// DecrementActiveConnections is a legacy method that is now a no-op
func (m *Metrics) DecrementActiveConnections() {
	// No-op as we're not tracking active connections anymore
}
