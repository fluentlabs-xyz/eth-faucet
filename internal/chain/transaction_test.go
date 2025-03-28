package chain

import (
	"context"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/backends"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestTxBuilder(t *testing.T) {
	privateKey, _ := crypto.HexToECDSA("976f9f7772781ff6d1c93941129d417c49a209c674056a3cf5e27e225ee55fa8")
	fromAddress := crypto.PubkeyToAddress(privateKey.PublicKey)
	simClient := backends.NewSimulatedBackend(
		core.GenesisAlloc{
			fromAddress: {Balance: big.NewInt(10000000000000000)},
		}, 10000000,
	)
	defer simClient.Close()
	var s *backends.SimulatedBackend
	patches := gomonkey.ApplyMethod(reflect.TypeOf(s), "SuggestGasPrice", func(_ *backends.SimulatedBackend, _ context.Context) (*big.Int, error) {
		return big.NewInt(875000000), nil
	})
	defer patches.Reset()

	txBuilder := &TxBuild{
		client:          simClient,
		privateKey:      privateKey,
		signer:          types.NewLondonSigner(big.NewInt(1337)),
		fromAddress:     crypto.PubkeyToAddress(privateKey.PublicKey),
		supportsEIP1559: false,
	}
	bgCtx := context.Background()
	toAddress := common.HexToAddress("0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B")
	value := big.NewInt(1000)
	txHash, err := txBuilder.Transfer(bgCtx, toAddress.Hex(), value)
	if err != nil {
		t.Errorf("could not add tx to pending block: %v", err)
	}
	simClient.Commit()

	block, err := simClient.BlockByNumber(bgCtx, big.NewInt(1))
	if err != nil {
		t.Errorf("could not get block at height 1: %v", err)
	}
	if txHash != block.Transactions()[0].Hash() {
		t.Errorf("did not commit sent transaction. expected hash %v got hash %v", block.Transactions()[0].Hash(), txHash)
	}

	bal, err := simClient.BalanceAt(bgCtx, toAddress, nil)
	if err != nil {
		t.Error(err)
	}
	if bal.Cmp(value) != 0 {
		t.Errorf("expected balance for to address not received. expected: %v actual: %v", value, bal)
	}
}

func TestFillMissedNonces(t *testing.T) {
	testCases := []struct {
		name string
		// RPC method return values
		confirmedNonce uint64 // eth_getTransactionCount ("latest") - Last confirmed nonce
		pendingNonce   uint64 // eth_getTransactionCount ("pending") - Next available nonce

		// txpool content
		pendingNonces []string // uint as string for pending nonces in txpool
		queuedNonces  []string // uint as string for queued nonces in txpool

		// Expected result
		expectedFilled []uint64 // Nonces that should be filled
	}{
		{
			name:           "Gap between pending and queued",
			confirmedNonce: 2,
			pendingNonce:   9,
			pendingNonces:  []string{"3", "4"},
			queuedNonces:   []string{"7", "8"},
			expectedFilled: []uint64{2, 5, 6},
		},
		{
			name:           "Pending transactions without gaps",
			confirmedNonce: 2,
			pendingNonce:   5,
			pendingNonces:  []string{"3", "4"},
			queuedNonces:   []string{},
			expectedFilled: []uint64{2},
		},
		{
			name:           "Multiple queued transactions with gap",
			confirmedNonce: 2,
			pendingNonce:   9,
			pendingNonces:  []string{},
			queuedNonces:   []string{"5", "8"},
			expectedFilled: []uint64{2, 3, 4, 6, 7},
		},
		{
			name:           "Gap from confirmed to queued",
			confirmedNonce: 2,
			pendingNonce:   5,
			pendingNonces:  []string{},
			queuedNonces:   []string{"5"},
			expectedFilled: []uint64{2, 3, 4},
		},
		{
			name:           "Large gap to queued",
			confirmedNonce: 2,
			pendingNonce:   11,
			pendingNonces:  []string{},
			queuedNonces:   []string{"10"},
			expectedFilled: []uint64{2, 3, 4, 5, 6, 7, 8, 9},
		},
		{
			name:           "Pending equals confirmed (no transactions in mempool)",
			confirmedNonce: 5,
			pendingNonce:   5,
			pendingNonces:  []string{},
			queuedNonces:   []string{},
			expectedFilled: []uint64{},
		},
		{
			name:           "No gap (pending follows confirmed)",
			confirmedNonce: 5,
			pendingNonce:   6,
			pendingNonces:  []string{},
			queuedNonces:   []string{"6"},
			expectedFilled: []uint64{5},
		},
		{
			name:           "No txpool transactions",
			confirmedNonce: 3,
			pendingNonce:   3,
			pendingNonces:  []string{},
			queuedNonces:   []string{},
			expectedFilled: []uint64{},
		},

		{
			name:           "No gaps with sequential transactions",
			confirmedNonce: 2,
			pendingNonce:   7,
			pendingNonces:  []string{"3", "4", "5", "6"},
			queuedNonces:   []string{},
			expectedFilled: []uint64{2},
		},
		{
			name:           "Gap in middle of pending nonces",
			confirmedNonce: 2,
			pendingNonce:   7,
			pendingNonces:  []string{"3", "5", "6"},
			queuedNonces:   []string{},
			expectedFilled: []uint64{2, 4},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			privateKey, _ := crypto.HexToECDSA("976f9f7772781ff6d1c93941129d417c49a209c674056a3cf5e27e225ee55fa8")
			fromAddress := crypto.PubkeyToAddress(privateKey.PublicKey)
			mockClient := &ethclient.Client{}
			mockRPC := &rpc.Client{}

			txBuilder := &TxBuild{
				client:      mockClient,
				rpcClient:   mockRPC,
				privateKey:  privateKey,
				fromAddress: fromAddress,
				signer:      types.NewLondonSigner(big.NewInt(1337)),
			}

			filledNonces := make(map[uint64]bool)

			patches := gomonkey.ApplyFunc(getTxCount,
				func(_ context.Context, _ *rpc.Client, _ common.Address) (uint64, error) {
					return tc.confirmedNonce, nil
				})
			defer patches.Reset()

			patches.ApplyMethod(reflect.TypeOf(mockRPC), "CallContext",
				func(_ *rpc.Client, _ context.Context, result interface{}, method string, args ...interface{}) error {
					if method == "txpool_content" {
						txpoolContent, ok := result.(*TxpoolContent)
						if !ok {
							return fmt.Errorf("unexpected result type")
						}

						addrStr := strings.ToLower(fromAddress.Hex())

						// pending transactions
						txpoolContent.Pending = make(map[string]map[string]interface{})
						txpoolContent.Pending[addrStr] = make(map[string]interface{})
						for _, nonceHex := range tc.pendingNonces {
							txpoolContent.Pending[addrStr][nonceHex] = make(map[string]interface{})
						}

						// queued transactions
						txpoolContent.Queued = make(map[string]map[string]interface{})
						txpoolContent.Queued[addrStr] = make(map[string]interface{})
						for _, nonceHex := range tc.queuedNonces {
							txpoolContent.Queued[addrStr][nonceHex] = make(map[string]interface{})
						}

						return nil
					}
					return fmt.Errorf("unexpected method call")
				})

			patches.ApplyMethod(reflect.TypeOf(mockClient), "PendingNonceAt",
				func(_ *ethclient.Client, _ context.Context, _ common.Address) (uint64, error) {
					return tc.pendingNonce, nil
				})

			patches.ApplyMethod(reflect.TypeOf(txBuilder), "SendTransactionWithNonce",
				func(_ *TxBuild, _ context.Context, nonce uint64) (common.Hash, error) {
					filledNonces[nonce] = true
					return common.HexToHash("0x123"), nil
				})

			err := txBuilder.FillMissedNonces(context.Background())
			if err != nil {
				t.Errorf("fillMissedNonces failed: %v", err)
			}

			for _, nonce := range tc.expectedFilled {
				if !filledNonces[nonce] {
					t.Errorf("expected nonce %d to be filled, but it wasn't", nonce)
				}
			}

			for nonce := range filledNonces {
				found := false
				for _, expected := range tc.expectedFilled {
					if nonce == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("nonce %d was filled but wasn't expected", nonce)
				}
			}
		})
	}
}
