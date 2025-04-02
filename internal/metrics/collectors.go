package metrics

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
)

// BalanceCollector implements prometheus.Collector interface for faucet balance
type BalanceCollector struct {
	client        *ethclient.Client
	address       common.Address
	faucetAddress string
	balanceDesc   *prometheus.Desc
}

// NewBalanceCollector creates a new collector for faucet balance
func NewBalanceCollector(providerURL string, address common.Address, faucetAddress string) (*BalanceCollector, error) {
	client, err := ethclient.Dial(providerURL)
	if err != nil {
		return nil, err
	}

	return &BalanceCollector{
		client:        client,
		address:       address,
		faucetAddress: faucetAddress,
		balanceDesc: prometheus.NewDesc(
			"faucet_balance_eth",
			"Current balance of the faucet account in ETH",
			[]string{"address"},
			nil,
		),
	}, nil
}

func (bc *BalanceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- bc.balanceDesc
}

func (bc *BalanceCollector) Collect(ch chan<- prometheus.Metric) {
	balance, err := bc.client.BalanceAt(context.Background(), bc.address, nil)
	if err != nil {
		log.WithError(err).Error("Failed to fetch faucet balance")
		return
	}

	// Convert to ETH
	balanceInEth := new(big.Float).Quo(
		new(big.Float).SetInt(balance),
		new(big.Float).SetInt(big.NewInt(1e18)),
	)
	ethValue, _ := balanceInEth.Float64()

	ch <- prometheus.MustNewConstMetric(
		bc.balanceDesc,
		prometheus.GaugeValue,
		ethValue,
		bc.faucetAddress,
	)

	log.WithField("balance_eth", ethValue).Debug("Updated faucet balance metric")
}

// NonceCollector implements prometheus.Collector interface for faucet nonces
type NonceCollector struct {
	client           *ethclient.Client
	address          common.Address
	faucetAddress    string
	latestNonceDesc  *prometheus.Desc
	pendingNonceDesc *prometheus.Desc
}

// NewNonceCollector creates a new collector for faucet nonces
func NewNonceCollector(providerURL string, address common.Address, faucetAddress string) (*NonceCollector, error) {
	client, err := ethclient.Dial(providerURL)
	if err != nil {
		return nil, err
	}

	return &NonceCollector{
		client:        client,
		address:       address,
		faucetAddress: faucetAddress,
		latestNonceDesc: prometheus.NewDesc(
			"faucet_nonce_latest",
			"Current latest nonce of the faucet account",
			[]string{"address"},
			nil,
		),
		pendingNonceDesc: prometheus.NewDesc(
			"faucet_nonce_pending",
			"Current pending nonce of the faucet account",
			[]string{"address"},
			nil,
		),
	}, nil
}

func (nc *NonceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- nc.latestNonceDesc
	ch <- nc.pendingNonceDesc
}

func (nc *NonceCollector) Collect(ch chan<- prometheus.Metric) {
	latestNonce, err := nc.client.NonceAt(context.Background(), nc.address, nil)
	if err != nil {
		log.WithError(err).Error("Failed to fetch latest nonce")
		return
	}

	ch <- prometheus.MustNewConstMetric(
		nc.latestNonceDesc,
		prometheus.GaugeValue,
		float64(latestNonce),
		nc.faucetAddress,
	)

	pendingNonce, err := nc.client.PendingNonceAt(context.Background(), nc.address)
	if err != nil {
		log.WithError(err).Error("Failed to fetch pending nonce")
		return
	}

	ch <- prometheus.MustNewConstMetric(
		nc.pendingNonceDesc,
		prometheus.GaugeValue,
		float64(pendingNonce),
		nc.faucetAddress,
	)

	log.WithFields(log.Fields{
		"latest_nonce":  latestNonce,
		"pending_nonce": pendingNonce,
	}).Debug("Updated nonce metrics")
}
