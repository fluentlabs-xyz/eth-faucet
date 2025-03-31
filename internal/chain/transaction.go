package chain

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	log "github.com/sirupsen/logrus"
)

type TxBuilder interface {
	Sender() common.Address
	Transfer(ctx context.Context, to string, value *big.Int) (common.Hash, error)
}

type TxBuild struct {
	client          bind.ContractTransactor
	rpcClient       *rpc.Client
	privateKey      *ecdsa.PrivateKey
	signer          types.Signer
	fromAddress     common.Address
	nonce           uint64
	supportsEIP1559 bool
}

func NewTxBuilder(provider string, privateKey *ecdsa.PrivateKey, chainID *big.Int) (TxBuilder, error) {
	// Create RPC client first
	rpcClient, err := rpc.Dial(provider)
	if err != nil {
		return nil, err
	}

	// Create Eth client
	client, err := ethclient.Dial(provider)
	if err != nil {
		return nil, err
	}

	if chainID == nil {
		chainID, err = client.ChainID(context.Background())
		if err != nil {
			return nil, err
		}
	}

	supportsEIP1559, err := checkEIP1559Support(client)
	if err != nil {
		return nil, err
	}

	txBuilder := &TxBuild{
		client:          client,
		rpcClient:       rpcClient,
		privateKey:      privateKey,
		signer:          types.NewLondonSigner(chainID),
		fromAddress:     crypto.PubkeyToAddress(privateKey.PublicKey),
		supportsEIP1559: supportsEIP1559,
	}

	// First fill any nonce gaps that might exist
	err = txBuilder.FillMissedNonces(context.Background())
	if err != nil {
		log.WithError(err).Warn("Failed to fill nonce gaps")
		return nil, err
	}

	// Then sync the nonce to ensure we have the latest value
	txBuilder.refreshNonce(context.Background())

	return txBuilder, nil
}

func (b *TxBuild) Sender() common.Address {
	return b.fromAddress
}

func (b *TxBuild) Transfer(ctx context.Context, to string, value *big.Int) (common.Hash, error) {
	gasLimit := uint64(21000)
	toAddress := common.HexToAddress(to)
	nonce := b.getAndIncrementNonce()

	var err error
	var unsignedTx *types.Transaction

	if b.supportsEIP1559 {
		unsignedTx, err = b.buildEIP1559Tx(ctx, &toAddress, value, gasLimit, nonce)
	} else {
		unsignedTx, err = b.buildLegacyTx(ctx, &toAddress, value, gasLimit, nonce)
	}

	if err != nil {
		return common.Hash{}, err
	}

	signedTx, err := types.SignTx(unsignedTx, b.signer, b.privateKey)
	if err != nil {
		return common.Hash{}, err
	}

	if err = b.client.SendTransaction(ctx, signedTx); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "nonce") ||
			strings.Contains(strings.ToLower(err.Error()), "underpriced") {
			// instead of refreshing nonce here, we should just return the error - after restart we will fill all missed nonces
			log.Fatal("Critical error:", err)
		}
		return common.Hash{}, err
	}

	go WatchReceipt(b.rpcClient, signedTx.Hash())

	return signedTx.Hash(), nil
}

func (b *TxBuild) buildEIP1559Tx(ctx context.Context, to *common.Address, value *big.Int, gasLimit uint64, nonce uint64) (*types.Transaction, error) {
	header, err := b.client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, err
	}

	gasTipCap, err := b.client.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, err
	}

	// gasFeeCap = baseFee * 2 + gasTipCap
	gasFeeCap := new(big.Int).Mul(header.BaseFee, big.NewInt(2))
	gasFeeCap = new(big.Int).Add(gasFeeCap, gasTipCap)

	return types.NewTx(&types.DynamicFeeTx{
		ChainID:   b.signer.ChainID(),
		Nonce:     nonce,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Gas:       gasLimit,
		To:        to,
		Value:     value,
	}), nil
}

func (b *TxBuild) buildLegacyTx(ctx context.Context, to *common.Address, value *big.Int, gasLimit uint64, nonce uint64) (*types.Transaction, error) {
	gasPrice, err := b.client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, err
	}

	return types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      gasLimit,
		To:       to,
		Value:    value,
	}), nil
}

func (b *TxBuild) getAndIncrementNonce() uint64 {
	return atomic.AddUint64(&b.nonce, 1) - 1
}

func (b *TxBuild) refreshNonce(ctx context.Context) {
	nonce, err := b.client.PendingNonceAt(ctx, b.Sender())
	if err != nil {
		log.WithFields(log.Fields{
			"address": b.Sender(),
			"error":   err,
		}).Error("failed to refresh account nonce")
		return
	}

	atomic.StoreUint64(&b.nonce, nonce)
}

func (b *TxBuild) SendTransactionWithNonce(ctx context.Context, nonce uint64) (common.Hash, error) {
	value := big.NewInt(0)
	gasLimit := uint64(21000)

	var err error
	var unsignedTx *types.Transaction

	if b.supportsEIP1559 {
		unsignedTx, err = b.buildEIP1559Tx(ctx, &b.fromAddress, value, gasLimit, nonce)
	} else {
		unsignedTx, err = b.buildLegacyTx(ctx, &b.fromAddress, value, gasLimit, nonce)
	}

	if err != nil {
		return common.Hash{}, err
	}

	signedTx, err := types.SignTx(unsignedTx, b.signer, b.privateKey)
	if err != nil {
		return common.Hash{}, err
	}

	if err = b.client.SendTransaction(ctx, signedTx); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "nonce") ||
			strings.Contains(strings.ToLower(err.Error()), "underpriced") {
			// instead of refreshing nonce here, we should just return the error - after restart we will fill all missed nonces
			log.Fatal("Critical error:", err)
		}
		return common.Hash{}, err
	}

	return signedTx.Hash(), nil
}

func (b *TxBuild) FillMissedNonces(ctx context.Context,
) error {
	confirmedNonce, err := getTxCount(ctx, b.rpcClient, b.Sender())
	if err != nil {
		return fmt.Errorf("failed to get confirmed nonce: %w", err)
	}

	pendingNonce, err := b.client.PendingNonceAt(ctx, b.Sender())
	if err != nil {
		return fmt.Errorf("failed to get pending nonce: %w", err)
	}

	if pendingNonce == confirmedNonce {
		log.Debug("No nonce gaps detected - confirmed and pending nonces are equals")
		return nil
	}

	txpoolContent, err := getTxpoolContent(ctx, b.rpcClient)
	if err != nil {
		return err
	}

	existingNonces, smallestQueuedNonce, hasQueuedTx := processTxpoolContent(txpoolContent, b.Sender())

	var gaps []uint64
	for nonce := confirmedNonce; nonce < pendingNonce; nonce++ {
		if !existingNonces[nonce] {
			gaps = append(gaps, nonce)
		}
	}

	if len(gaps) == 0 {
		log.Info("No nonce gaps to fill")
		return nil
	}

	// Log the gaps we're going to fill
	log.WithFields(log.Fields{
		"confirmedNonce":      confirmedNonce,
		"pendingNonce":        pendingNonce,
		"hasQueuedTx":         hasQueuedTx,
		"smallestQueuedNonce": smallestQueuedNonce,
		"gapCount":            len(gaps),
	}).Warn("Filling nonce gaps")

	for _, nonce := range gaps {
		txHash, err := b.SendTransactionWithNonce(ctx, nonce)
		if err != nil {
			err := fmt.Errorf("failed to fill nonce gap %d: %w", nonce, err)
			// If we fail to fill the gap, we should restart
			log.Fatalf("Critical error: %v", err)
		}

		log.WithFields(log.Fields{
			"nonce":  nonce,
			"txHash": txHash.Hex(),
		}).Info("Successfully filled missed nonce")
	}

	return nil
}

func checkEIP1559Support(client *ethclient.Client) (bool, error) {
	header, err := client.HeaderByNumber(context.Background(), nil)
	if err != nil {
		return false, err
	}

	return header.BaseFee != nil && header.BaseFee.Cmp(big.NewInt(0)) > 0, nil
}

// TxpoolContent represents the structure of the txpool_content RPC response
type TxpoolContent struct {
	Pending map[string]map[string]interface{} `json:"pending"`
	Queued  map[string]map[string]interface{} `json:"queued"`
}

func getTxpoolContent(ctx context.Context, rpcClient *rpc.Client) (*TxpoolContent, error) {
	var txpoolContent TxpoolContent
	err := rpcClient.CallContext(ctx, &txpoolContent, "txpool_content")
	if err != nil {
		return nil, fmt.Errorf("failed to get txpool content: %w", err)
	}
	return &txpoolContent, nil
}

func processTxpoolContent(txpoolContent *TxpoolContent, address common.Address) (
	existingNonces map[uint64]bool, smallestQueuedNonce uint64, hasQueuedTx bool,
) {
	addrStr := strings.ToLower(address.Hex())
	existingNonces = make(map[uint64]bool)
	smallestQueuedNonce = ^uint64(0) // max uint64 value
	hasQueuedTx = false

	if pendingTxs, exists := txpoolContent.Pending[addrStr]; exists {
		for nonceStr := range pendingTxs {
			nonce, err := strconv.ParseUint(nonceStr, 10, 64)
			if err != nil {
				continue
			}
			existingNonces[nonce] = true
		}
	}

	if queuedTxs, exists := txpoolContent.Queued[addrStr]; exists {
		for nonceStr := range queuedTxs {

			nonce, err := strconv.ParseUint(nonceStr, 10, 64)
			if err != nil {
				continue
			}
			existingNonces[nonce] = true

			if !hasQueuedTx || nonce < smallestQueuedNonce {
				smallestQueuedNonce = nonce
				hasQueuedTx = true
			}
		}
	}

	return existingNonces, smallestQueuedNonce, hasQueuedTx
}

// tx count == next nonce
func getTxCount(ctx context.Context, rpc *rpc.Client, address common.Address) (uint64, error) {
	var nonce uint64
	err := rpc.CallContext(ctx, &nonce, "eth_getTransactionCount", address.Hex(), "latest")
	if err != nil {
		return 0, fmt.Errorf("failed to get pending nonce: %w", err)
	}
	return nonce, nil
}

// WatchReceipt monitors transaction receipt for 15 seconds
func WatchReceipt(client *rpc.Client, txHash common.Hash) {
	log.WithField("txHash", txHash.Hex()).Info("Monitoring transaction receipt")

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timer.C:
			log.WithField("txHash", txHash.Hex()).Error("Receipt not received in 15 seconds, exiting")
			os.Exit(1)

		case <-ticker.C:
			var receipt *types.Receipt
			err := client.CallContext(context.Background(), &receipt, "eth_getTransactionReceipt", txHash)
			if err != nil || receipt == nil {
				continue
			}

			log.WithFields(log.Fields{
				"txHash":      txHash.Hex(),
				"blockNumber": receipt.BlockNumber,
				"status":      receipt.Status,
			}).Info("Transaction confirmed")
			return
		}
	}
}
