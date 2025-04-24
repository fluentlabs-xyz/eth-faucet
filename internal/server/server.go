package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/urfave/negroni/v3"

	"github.com/chainflag/eth-faucet/internal/chain"
	"github.com/chainflag/eth-faucet/internal/metrics"
	"github.com/chainflag/eth-faucet/web"
)

type Server struct {
	chain.TxBuilder
	cfg     *Config
	metrics *metrics.Metrics
}

func NewServer(builder chain.TxBuilder, cfg *Config) *Server {
	s := &Server{
		TxBuilder: builder,
		cfg:       cfg,
	}

	// Initialize metrics if enabled
	if cfg.metricsPort > 0 {
		s.metrics = metrics.NewMetrics("faucet", s.Sender().Hex(), cfg.providerURL, s.Sender())
	}

	return s
}

func (s *Server) setupRouter() *http.ServeMux {
	router := http.NewServeMux()

	if !s.cfg.apiOnly {
		router.Handle("/", http.FileServer(web.Dist()))
	} else {
		log.Info("Running in API-only mode")
	}

	limiter := NewLimiter(s.cfg.proxyCount, time.Duration(s.cfg.interval)*time.Minute, s.metrics)
	hcaptcha := NewCaptcha(s.cfg.hcaptchaSiteKey, s.cfg.hcaptchaSecret, s.metrics)
	metricsMiddleware := NewRequestMetrics(s.metrics)

	claimMiddleware := []negroni.Handler{metricsMiddleware, limiter, hcaptcha}
	infoMiddleware := []negroni.Handler{metricsMiddleware}

	// Add CORS middleware if in API-only mode
	if s.cfg.apiOnly {
		corsMiddleware := NewCorsMiddleware(s.cfg.corsAllowed, s.cfg.corsHeaders, s.cfg.corsMethods)
		claimMiddleware = append([]negroni.Handler{corsMiddleware}, claimMiddleware...)
		infoMiddleware = append([]negroni.Handler{corsMiddleware}, infoMiddleware...)
		log.Infof("CORS enabled with allowed origins: %s", s.cfg.corsAllowed)
	}

	// Apply middleware chain to API endpoints
	router.Handle("/api/claim", negroni.New(append(claimMiddleware, negroni.Wrap(s.handleClaim()))...))
	router.Handle("/api/info", negroni.New(append(infoMiddleware, negroni.Wrap(s.handleInfo()))...))

	return router
}

func (s *Server) setupMetricsServer() {
	if s.cfg.metricsPort <= 0 || s.metrics == nil {
		return
	}

	if err := s.metrics.StartServer(s.cfg.metricsPort, s.cfg.metricsPath); err != nil {
		log.Fatal(err)
	}
	log.Infof("Prometheus metrics enabled on port %d, path %s", s.cfg.metricsPort, s.cfg.metricsPath)
}

func (s *Server) Run() {
	s.setupMetricsServer()

	n := negroni.New(negroni.NewRecovery(), negroni.NewLogger())
	n.UseHandler(s.setupRouter())
	log.Infof("Starting http server %d", s.cfg.httpPort)
	log.Fatal(http.ListenAndServe(":"+strconv.Itoa(s.cfg.httpPort), n))
}

func (s *Server) handleClaim() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}

		// The error always be nil since it has already been handled in limiter
		address, _ := readAddress(r)
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		txHash, err := s.Transfer(ctx, address, chain.EtherToWei(s.cfg.payout))
		if err != nil {
			log.WithError(err).Error("Failed to send transaction")
			renderJSON(w, claimResponse{Message: err.Error()}, http.StatusInternalServerError)
			return
		}

		log.WithFields(log.Fields{
			"txHash":  txHash,
			"address": address,
		}).Info("Transaction sent successfully")
		resp := claimResponse{Message: fmt.Sprintf("Txhash: %s", txHash)}
		renderJSON(w, resp, http.StatusOK)
	}
}

func (s *Server) handleInfo() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		renderJSON(w, infoResponse{
			Account:         s.Sender().String(),
			Network:         s.cfg.network,
			Symbol:          s.cfg.symbol,
			Payout:          strconv.FormatFloat(s.cfg.payout, 'f', -1, 64),
			HcaptchaSiteKey: s.cfg.hcaptchaSiteKey,
		}, http.StatusOK)
	}
}
