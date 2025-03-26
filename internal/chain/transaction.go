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

	txBuilder.refreshNonce(context.Background())
	txBuilder.fillMissedNonces(context.Background())

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
		if strings.Contains(strings.ToLower(err.Error()), "nonce") {
			b.refreshNonce(context.Background())
			b.fillMissedNonces(context.Background())
		}
		return common.Hash{}, err
	}

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

func checkEIP1559Support(client *ethclient.Client) (bool, error) {
	header, err := client.HeaderByNumber(context.Background(), nil)
	if err != nil {
		return false, err
	}

	return header.BaseFee != nil && header.BaseFee.Cmp(big.NewInt(0)) > 0, nil
}

func (b *TxBuild) getSmallestQueuedNonce(ctx context.Context) (uint64, bool, error) {
	var txpoolContent TxpoolContent
	err := b.rpcClient.CallContext(ctx, &txpoolContent, "txpool_content")
	if err != nil {
		return 0, false, fmt.Errorf("failed to get txpool content: %w", err)
	}

	// Address in lowercase for comparison
	addrStr := strings.ToLower(b.fromAddress.Hex())

	// Look only at queued transactions
	if queuedTxs, exists := txpoolContent.Queued[addrStr]; exists {
		smallestNonce := ^uint64(0) // Max uint64 value
		hasNonce := false

		for nonceHex := range queuedTxs {
			nonceInt, err := strconv.ParseUint(nonceHex, 16, 64)
			if err != nil {
				continue
			}

			if !hasNonce || nonceInt < smallestNonce {
				smallestNonce = nonceInt
				hasNonce = true
			}
		}

		return smallestNonce, hasNonce, nil
	}

	return 0, false, nil // No queued transactions
}

// fillMissedNonce fills a single missed nonce by sending a self-transaction
func (b *TxBuild) fillMissedNonce(ctx context.Context, nonce uint64) error {
	tx, err := b.SendSelfTransaction(ctx, nonce)
	if err != nil {
		log.WithFields(log.Fields{
			"nonce": nonce,
			"error": err,
		}).Error("Failed to fill missed nonce")
		return err
	}

	log.WithFields(log.Fields{
		"nonce":  nonce,
		"txHash": tx.Hash().Hex(),
	}).Info("Successfully filled missed nonce")

	return nil
}

// fillMissedNonces detects and fills nonce gaps for our account
func (b *TxBuild) fillMissedNonces(ctx context.Context) error {
	// Cast client to ethclient.Client
	ethClient, ok := b.client.(*ethclient.Client)
	if !ok {
		return fmt.Errorf("client is not an ethclient.Client")
	}

	// Get confirmed nonce
	confirmedNonce, err := ethClient.NonceAt(ctx, b.fromAddress, nil)
	if err != nil {
		return fmt.Errorf("failed to get confirmed nonce: %w", err)
	}

	// Get smallest queued nonce
	smallestQueuedNonce, hasQueuedTx, err := b.getSmallestQueuedNonce(ctx)
	if err != nil {
		return err
	}

	// Update our internal nonce counter
	pendingNonce, err := ethClient.PendingNonceAt(ctx, b.fromAddress)
	if err != nil {
		return fmt.Errorf("failed to get pending nonce: %w", err)
	}

	if pendingNonce > atomic.LoadUint64(&b.nonce) {
		atomic.StoreUint64(&b.nonce, pendingNonce)
	}

	// If no queued transactions or smallest queued nonce is not greater than confirmed, nothing to fill
	if !hasQueuedTx || smallestQueuedNonce <= confirmedNonce {
		log.Info("No nonce gaps to fill")
		return nil
	}

	log.WithFields(log.Fields{
		"confirmedNonce":      confirmedNonce,
		"smallestQueuedNonce": smallestQueuedNonce,
		"pendingNonce":        pendingNonce,
	}).Info("Checking for nonce gaps")

	// Directly fill gaps between confirmed and smallest queued nonce
	gapCount := smallestQueuedNonce - confirmedNonce
	if gapCount > 0 {
		log.WithFields(log.Fields{
			"count": gapCount,
			"from":  confirmedNonce,
			"to":    smallestQueuedNonce - 1,
		}).Warn("Filling nonce gaps")

		// Fill each gap using sendSelfTransaction
		for nonce := confirmedNonce; nonce < smallestQueuedNonce; nonce++ {
			tx, err := b.SendSelfTransaction(ctx, nonce)
			if err != nil {
				log.WithFields(log.Fields{
					"nonce": nonce,
					"error": err,
				}).Error("Failed to fill nonce gap")
				continue // Continue with other nonces even if one fails
			}

			log.WithFields(log.Fields{
				"nonce":  nonce,
				"txHash": tx.Hash().Hex(),
			}).Info("Successfully filled missed nonce")
		}
	}

	return nil
}

// SendSelfTransaction sends a minimal EIP-1559 transaction to self with the specified nonce
func (b *TxBuild) SendSelfTransaction(ctx context.Context, nonce uint64) (*types.Transaction, error) {
	// Minimal value (0 ETH)
	value := big.NewInt(0)

	// Standard gas limit for simple transfer
	gasLimit := uint64(21000)

	ethClient, ok := b.client.(*ethclient.Client)
	if !ok {
		return nil, fmt.Errorf("client is not an ethclient.Client")
	}

	var tx *types.Transaction
	var err error

	if b.supportsEIP1559 {
		// Get current header for baseFee
		header, err := ethClient.HeaderByNumber(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get header: %w", err)
		}

		// Get suggested tip cap
		gasTipCap, err := ethClient.SuggestGasTipCap(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to suggest gas tip cap: %w", err)
		}

		// Boost by 20% to ensure quick inclusion
		gasTipCap = new(big.Int).Mul(gasTipCap, big.NewInt(120))
		gasTipCap = new(big.Int).Div(gasTipCap, big.NewInt(100))

		// gasFeeCap = baseFee * 2 + gasTipCap
		gasFeeCap := new(big.Int).Mul(header.BaseFee, big.NewInt(2))
		gasFeeCap = new(big.Int).Add(gasFeeCap, gasTipCap)

		// Create transaction
		tx = types.NewTx(&types.DynamicFeeTx{
			ChainID:   b.signer.ChainID(),
			Nonce:     nonce,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Gas:       gasLimit,
			To:        &b.fromAddress, // Send to self
			Value:     value,
		})
	} else {
		// For legacy transactions
		gasPrice, err := ethClient.SuggestGasPrice(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to suggest gas price: %w", err)
		}

		// Boost by 20%
		gasPrice = new(big.Int).Mul(gasPrice, big.NewInt(120))
		gasPrice = new(big.Int).Div(gasPrice, big.NewInt(100))

		tx = types.NewTx(&types.LegacyTx{
			Nonce:    nonce,
			GasPrice: gasPrice,
			Gas:      gasLimit,
			To:       &b.fromAddress, // Send to self
			Value:    value,
		})
	}

	// Sign and send
	signedTx, err := types.SignTx(tx, b.signer, b.privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	if err = b.client.SendTransaction(ctx, signedTx); err != nil {
		return nil, fmt.Errorf("failed to send transaction: %w", err)
	}

	return signedTx, nil
}
