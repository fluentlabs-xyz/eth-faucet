package chain

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync/atomic"

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

// TxpoolContent represents the structure of the txpool_content RPC response
type TxpoolContent struct {
	Pending map[string]map[string]interface{} `json:"pending"`
	Queued  map[string]map[string]interface{} `json:"queued"`
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
	err = FillMissedNonces(context.Background(), client, rpcClient, txBuilder.fromAddress, txBuilder.signer, txBuilder.privateKey, txBuilder.supportsEIP1559)
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

	// Handle different client types
	switch client := b.client.(type) {
	case *ethclient.Client:
		if b.supportsEIP1559 {
			unsignedTx, err = createEIP1559Tx(ctx, client, b.signer.ChainID(), nonce, &toAddress, value, gasLimit)
		} else {
			unsignedTx, err = createLegacyTx(ctx, client, nonce, &toAddress, value, gasLimit)
		}
	default:
		// For other client types (like SimulatedBackend in tests), use the client's methods directly
		if b.supportsEIP1559 {
			header, err := b.client.HeaderByNumber(ctx, nil)
			if err != nil {
				return common.Hash{}, err
			}

			gasTipCap, err := b.client.SuggestGasTipCap(ctx)
			if err != nil {
				return common.Hash{}, err
			}

			gasFeeCap := new(big.Int).Mul(header.BaseFee, big.NewInt(2))
			gasFeeCap = new(big.Int).Add(gasFeeCap, gasTipCap)

			unsignedTx = types.NewTx(&types.DynamicFeeTx{
				ChainID:   b.signer.ChainID(),
				Nonce:     nonce,
				GasTipCap: gasTipCap,
				GasFeeCap: gasFeeCap,
				Gas:       gasLimit,
				To:        &toAddress,
				Value:     value,
			})
		} else {
			gasPrice, err := b.client.SuggestGasPrice(ctx)
			if err != nil {
				return common.Hash{}, err
			}

			unsignedTx = types.NewTx(&types.LegacyTx{
				Nonce:    nonce,
				GasPrice: gasPrice,
				Gas:      gasLimit,
				To:       &toAddress,
				Value:    value,
			})
		}
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

func (b *TxBuild) getAndIncrementNonce() uint64 {
	return atomic.AddUint64(&b.nonce, 1) - 1
}

func (b *TxBuild) refreshNonce(ctx context.Context) {
	ethClient, ok := b.client.(*ethclient.Client)
	if !ok {
		log.Error("client is not an ethclient.Client")
		return
	}

	nonce, err := ethClient.PendingNonceAt(ctx, b.Sender())
	if err != nil {
		log.WithFields(log.Fields{
			"address": b.Sender(),
			"error":   err,
		}).Error("failed to refresh account nonce")
		return
	}

	atomic.StoreUint64(&b.nonce, nonce)
}

func checkEIP1559Support(client *ethclient.Client) (bool, error) {
	header, err := client.HeaderByNumber(context.Background(), nil)
	if err != nil {
		return false, err
	}

	return header.BaseFee != nil && header.BaseFee.Cmp(big.NewInt(0)) > 0, nil
}

func createEIP1559Tx(ctx context.Context, client *ethclient.Client, chainID *big.Int,
	nonce uint64, to *common.Address, value *big.Int, gasLimit uint64,
) (*types.Transaction, error) {
	header, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, err
	}

	gasTipCap, err := client.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, err
	}

	// Boost by 20% to ensure quick inclusion
	gasTipCap = new(big.Int).Mul(gasTipCap, big.NewInt(120))
	gasTipCap = new(big.Int).Div(gasTipCap, big.NewInt(100))

	// gasFeeCap = baseFee * 2 + gasTipCap
	gasFeeCap := new(big.Int).Mul(header.BaseFee, big.NewInt(2))
	gasFeeCap = new(big.Int).Add(gasFeeCap, gasTipCap)

	return types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Gas:       gasLimit,
		To:        to,
		Value:     value,
	}), nil
}

func createLegacyTx(ctx context.Context, client *ethclient.Client,
	nonce uint64, to *common.Address, value *big.Int, gasLimit uint64,
) (*types.Transaction, error) {
	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, err
	}

	// Boost by 20%
	gasPrice = new(big.Int).Mul(gasPrice, big.NewInt(120))
	gasPrice = new(big.Int).Div(gasPrice, big.NewInt(100))

	return types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      gasLimit,
		To:       to,
		Value:    value,
	}), nil
}

func sendTransactionWithNonce(ctx context.Context, client *ethclient.Client, address common.Address,
	signer types.Signer, privateKey *ecdsa.PrivateKey, nonce uint64,
	supportsEIP1559 bool,
) (*types.Transaction, error) {
	value := big.NewInt(0)
	gasLimit := uint64(21000)

	var tx *types.Transaction
	var err error

	if supportsEIP1559 {
		tx, err = createEIP1559Tx(ctx, client, signer.ChainID(), nonce, &address, value, gasLimit)
	} else {
		tx, err = createLegacyTx(ctx, client, nonce, &address, value, gasLimit)
	}

	if err != nil {
		return nil, err
	}

	signedTx, err := types.SignTx(tx, signer, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	if err = client.SendTransaction(ctx, signedTx); err != nil {
		return nil, fmt.Errorf("failed to send transaction: %w", err)
	}

	return signedTx, nil
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

// FillMissedNonces detects and fills nonce gaps for the account
func FillMissedNonces(ctx context.Context, client *ethclient.Client, rpcClient *rpc.Client,
	address common.Address, signer types.Signer, privateKey *ecdsa.PrivateKey,
	supportsEIP1559 bool,
) error {
	confirmedNonce, err := client.NonceAt(ctx, address, nil)
	if err != nil {
		return fmt.Errorf("failed to get confirmed nonce: %w", err)
	}

	pendingNonce, err := client.PendingNonceAt(ctx, address)
	if err != nil {
		return fmt.Errorf("failed to get pending nonce: %w", err)
	}

	if pendingNonce == confirmedNonce {
		log.Debug("No nonce gaps detected - confirmed and pending nonces are equals")
		return nil
	}

	txpoolContent, err := getTxpoolContent(ctx, rpcClient)
	if err != nil {
		return err
	}

	existingNonces, smallestQueuedNonce, hasQueuedTx := processTxpoolContent(txpoolContent, address)

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
		tx, err := sendTransactionWithNonce(ctx, client, address, signer, privateKey, nonce, supportsEIP1559)
		if err != nil {
			err := fmt.Errorf("failed to fill nonce gap %d: %w", nonce, err)
			// If we fail to fill the gap, we should restart
			log.Fatalf("Critical error: %v", err)
		}

		log.WithFields(log.Fields{
			"nonce":  nonce,
			"txHash": tx.Hash().Hex(),
		}).Info("Successfully filled missed nonce")
	}

	return nil
}
