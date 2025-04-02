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
}

func NewConfig(network, symbol string, httpPort, interval, proxyCount int, payout float64, hcaptchaSiteKey, hcaptchaSecret string, metricsPort int, metricsPath string, providerURL string) *Config {
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
	}
}
