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

func TestGetSmallestQueuedNonce(t *testing.T) {
	// Set up a basic TxBuild instance
	privateKey, _ := crypto.HexToECDSA("976f9f7772781ff6d1c93941129d417c49a209c674056a3cf5e27e225ee55fa8")
	fromAddress := crypto.PubkeyToAddress(privateKey.PublicKey)

	// Create a mock RPC client
	mockRPC := &rpc.Client{}

	txBuilder := &TxBuild{
		privateKey:  privateKey,
		fromAddress: fromAddress,
		rpcClient:   mockRPC,
	}

	// Mock the RPC CallContext to return a predefined txpool content
	patches := gomonkey.ApplyMethod(reflect.TypeOf(mockRPC), "CallContext",
		func(_ *rpc.Client, _ context.Context, result interface{}, method string, args ...interface{}) error {
			if method == "txpool_content" {
				// Cast result to TxpoolContent
				txpoolContent, ok := result.(*TxpoolContent)
				if !ok {
					return fmt.Errorf("unexpected result type")
				}

				// Set up a scenario with queued transactions
				addrStr := strings.ToLower(fromAddress.Hex())
				txpoolContent.Queued = make(map[string]map[string]interface{})
				txpoolContent.Queued[addrStr] = make(map[string]interface{})

				// Add transactions with different nonces
				txpoolContent.Queued[addrStr]["a"] = make(map[string]interface{}) // Invalid nonce
				txpoolContent.Queued[addrStr]["5"] = make(map[string]interface{}) // Nonce 5
				txpoolContent.Queued[addrStr]["3"] = make(map[string]interface{}) // Nonce 3 (smallest)
				txpoolContent.Queued[addrStr]["7"] = make(map[string]interface{}) // Nonce 7

				return nil
			}
			return fmt.Errorf("unexpected method call")
		})
	defer patches.Reset()

	// Call getSmallestQueuedNonce
	nonce, hasNonce, err := txBuilder.getSmallestQueuedNonce(context.Background())
	// Verify results
	if err != nil {
		t.Errorf("getSmallestQueuedNonce failed: %v", err)
	}
	if !hasNonce {
		t.Errorf("expected hasNonce to be true")
	}
	if nonce != 3 {
		t.Errorf("expected smallest nonce to be 3, got %d", nonce)
	}
}

func TestFillMissedNoncesLogic(t *testing.T) {
	// Set up a basic TxBuild instance with mocks
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

	// Track which nonces were filled
	filledNonces := make(map[uint64]bool)

	// Mock the RPC CallContext to return a predefined txpool content
	patches := gomonkey.ApplyMethod(reflect.TypeOf(mockRPC), "CallContext",
		func(_ *rpc.Client, _ context.Context, result interface{}, method string, args ...interface{}) error {
			if method == "txpool_content" {
				// Cast result to TxpoolContent
				txpoolContent, ok := result.(*TxpoolContent)
				if !ok {
					return fmt.Errorf("unexpected result type")
				}

				// Set up a scenario with queued transactions
				addrStr := strings.ToLower(fromAddress.Hex())
				txpoolContent.Queued = make(map[string]map[string]interface{})
				txpoolContent.Queued[addrStr] = make(map[string]interface{})

				// Add a transaction with nonce 5
				txpoolContent.Queued[addrStr]["5"] = make(map[string]interface{})

				return nil
			}
			return fmt.Errorf("unexpected method call")
		})
	defer patches.Reset()

	patches.ApplyMethod(reflect.TypeOf(mockClient), "NonceAt",
		func(_ *ethclient.Client, _ context.Context, _ common.Address, _ *big.Int) (uint64, error) {
			return 2, nil // Confirmed nonce is 2
		})

	patches.ApplyMethod(reflect.TypeOf(mockClient), "PendingNonceAt",
		func(_ *ethclient.Client, _ context.Context, _ common.Address) (uint64, error) {
			return 2, nil // Pending nonce is 2
		})

	// Now we can directly mock the public SendSelfTransaction method
	patches.ApplyMethod(reflect.TypeOf(txBuilder), "SendSelfTransaction",
		func(_ *TxBuild, _ context.Context, nonce uint64) (*types.Transaction, error) {
			filledNonces[nonce] = true

			// Create a properly initialized transaction
			tx := types.NewTx(&types.LegacyTx{
				Nonce:    nonce,
				GasPrice: big.NewInt(1),
				Gas:      21000,
				To:       &fromAddress, // Send to self
				Value:    big.NewInt(0),
				Data:     nil,
			})

			return tx, nil
		})

	// Call fillMissedNonces
	err := txBuilder.fillMissedNonces(context.Background())
	// Verify results
	if err != nil {
		t.Errorf("fillMissedNonces failed: %v", err)
	}

	// Check that nonces 2, 3, 4 were filled
	expectedNonces := []uint64{2, 3, 4}
	for _, nonce := range expectedNonces {
		if !filledNonces[nonce] {
			t.Errorf("expected nonce %d to be filled, but it wasn't", nonce)
		}
	}

	// Check that nonce 5 was not filled (it's the queued one)
	if filledNonces[5] {
		t.Errorf("nonce 5 should not have been filled")
	}
}

func TestFillMissedNoncesTable(t *testing.T) {
	testCases := []struct {
		name           string
		confirmedNonce uint64
		queuedNonces   []string // Hex strings for nonces
		pendingNonce   uint64
		expectedFilled []uint64
	}{
		{
			name:           "Gap from 2 to 5",
			confirmedNonce: 2,
			queuedNonces:   []string{"5"},
			pendingNonce:   5,
			expectedFilled: []uint64{2, 3, 4},
		},
		{
			name:           "No gap (confirmed equals queued)",
			confirmedNonce: 5,
			queuedNonces:   []string{"5"},
			pendingNonce:   5,
			expectedFilled: []uint64{},
		},
		{
			name:           "No queued transactions",
			confirmedNonce: 3,
			queuedNonces:   []string{},
			pendingNonce:   3,
			expectedFilled: []uint64{},
		},
		{
			name:           "Multiple queued transactions (smallest is 5)",
			confirmedNonce: 2,
			queuedNonces:   []string{"5", "8"},
			pendingNonce:   2,
			expectedFilled: []uint64{2, 3, 4},
		},
		{
			name:           "Large gap",
			confirmedNonce: 2,
			queuedNonces:   []string{"a"},
			pendingNonce:   2,
			expectedFilled: []uint64{2, 3, 4, 5, 6, 7, 8, 9},
		},
		{
			name:           "Confirmed higher than queued (unusual case)",
			confirmedNonce: 7,
			queuedNonces:   []string{"5"},
			pendingNonce:   7,
			expectedFilled: []uint64{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set up a basic TxBuild instance with mocks
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

			// Track which nonces were filled
			filledNonces := make(map[uint64]bool)

			// Mock the RPC CallContext to return a predefined txpool content
			patches := gomonkey.ApplyMethod(reflect.TypeOf(mockRPC), "CallContext",
				func(_ *rpc.Client, _ context.Context, result interface{}, method string, args ...interface{}) error {
					if method == "txpool_content" {
						// Cast result to TxpoolContent
						txpoolContent, ok := result.(*TxpoolContent)
						if !ok {
							return fmt.Errorf("unexpected result type")
						}

						// Set up a scenario with queued transactions
						addrStr := strings.ToLower(fromAddress.Hex())
						txpoolContent.Queued = make(map[string]map[string]interface{})
						txpoolContent.Queued[addrStr] = make(map[string]interface{})

						// Add queued transactions based on test case
						for _, nonceHex := range tc.queuedNonces {
							txpoolContent.Queued[addrStr][nonceHex] = make(map[string]interface{})
						}

						return nil
					}
					return fmt.Errorf("unexpected method call")
				})
			defer patches.Reset()

			patches.ApplyMethod(reflect.TypeOf(mockClient), "NonceAt",
				func(_ *ethclient.Client, _ context.Context, _ common.Address, _ *big.Int) (uint64, error) {
					return tc.confirmedNonce, nil
				})

			patches.ApplyMethod(reflect.TypeOf(mockClient), "PendingNonceAt",
				func(_ *ethclient.Client, _ context.Context, _ common.Address) (uint64, error) {
					return tc.pendingNonce, nil
				})

			// Mock the SendSelfTransaction method
			patches.ApplyMethod(reflect.TypeOf(txBuilder), "SendSelfTransaction",
				func(_ *TxBuild, _ context.Context, nonce uint64) (*types.Transaction, error) {
					filledNonces[nonce] = true

					// Create a properly initialized transaction
					tx := types.NewTx(&types.LegacyTx{
						Nonce:    nonce,
						GasPrice: big.NewInt(1),
						Gas:      21000,
						To:       &fromAddress,
						Value:    big.NewInt(0),
						Data:     nil,
					})

					return tx, nil
				})

			// Call fillMissedNonces
			err := txBuilder.fillMissedNonces(context.Background())
			// Verify results
			if err != nil {
				t.Errorf("fillMissedNonces failed: %v", err)
			}

			// Check that expected nonces were filled
			for _, nonce := range tc.expectedFilled {
				if !filledNonces[nonce] {
					t.Errorf("expected nonce %d to be filled, but it wasn't", nonce)
				}
			}

			// Check that no unexpected nonces were filled
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
