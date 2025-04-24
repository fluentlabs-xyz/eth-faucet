package server

type Config struct {
	network         string
	symbol          string
	httpPort        int
	interval        int
	payout          float64
	proxyCount      int
	hcaptchaSiteKey string
	hcaptchaSecret  string
	metricsPort     int
	metricsPath     string
	providerURL     string

	// API-only mode configuration
	apiOnly     bool
	corsAllowed string
	corsHeaders string
	corsMethods string
}

func NewConfig(network, symbol string, httpPort, interval, proxyCount int, payout float64, hcaptchaSiteKey, hcaptchaSecret string, metricsPort int, metricsPath string, providerURL string, apiOnly bool, corsAllowed, corsHeaders, corsMethods string) *Config {
	return &Config{
		network:         network,
		symbol:          symbol,
		httpPort:        httpPort,
		interval:        interval,
		payout:          payout,
		proxyCount:      proxyCount,
		hcaptchaSiteKey: hcaptchaSiteKey,
		hcaptchaSecret:  hcaptchaSecret,
		metricsPort:     metricsPort,
		metricsPath:     metricsPath,
		providerURL:     providerURL,

		// API-only mode configuration
		apiOnly:     apiOnly,
		corsAllowed: corsAllowed,
		corsHeaders: corsHeaders,
		corsMethods: corsMethods,
	}
}
