package jsonrpc

import (
	"bytes"
	"fmt"
	"math/big"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"

	"github.com/0xPolygon/minimal/helper/hex"
	"github.com/0xPolygon/minimal/state"
	"github.com/0xPolygon/minimal/types"
)

type mockAccount2 struct {
	store   *mockAccountStore
	address types.Address
	code    []byte
	account *state.Account
	storage map[types.Hash]types.Hash
}

func (m *mockAccount2) Storage(k, v types.Hash) {
	m.storage[k] = v
}

func (m *mockAccount2) Code(code []byte) {
	codeHash := types.BytesToHash(m.address.Bytes())
	m.code = code
	m.account.CodeHash = codeHash.Bytes()
}

func (m *mockAccount2) Nonce(n uint64) {
	m.account.Nonce = n
}

func (m *mockAccount2) Balance(n uint64) {
	m.account.Balance = new(big.Int).SetUint64(n)
}

type mockAccountStore struct {
	nullBlockchainInterface
	accounts map[types.Address]*mockAccount2
}

func (m *mockAccountStore) AddAccount(addr types.Address) *mockAccount2 {
	if m.accounts == nil {
		m.accounts = map[types.Address]*mockAccount2{}
	}
	acct := &mockAccount2{
		store:   m,
		address: addr,
		account: &state.Account{},
		storage: map[types.Hash]types.Hash{},
	}
	m.accounts[addr] = acct
	return acct
}

func (m *mockAccountStore) Header() *types.Header {
	return &types.Header{}
}

func (m *mockAccountStore) GetAccount(root types.Hash, addr types.Address) (*state.Account, error) {
	acct, ok := m.accounts[addr]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return acct.account, nil
}

func (m *mockAccountStore) GetCode(hash types.Hash) ([]byte, error) {
	for _, acct := range m.accounts {
		if bytes.Equal(acct.account.CodeHash, hash.Bytes()) {
			return acct.code, nil
		}
	}
	return nil, fmt.Errorf("code not found")
}

func (m *mockAccountStore) GetStorage(root types.Hash, addr types.Address, slot types.Hash) ([]byte, error) {
	acct, ok := m.accounts[addr]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	val, ok := acct.storage[slot]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return val.Bytes(), nil
}

type mockBlockStore2 struct {
	nullBlockchainInterface
	blocks []*types.Block
}

func (m *mockBlockStore2) add(blocks ...*types.Block) {
	if m.blocks == nil {
		m.blocks = []*types.Block{}
	}
	m.blocks = append(m.blocks, blocks...)
}

func (m *mockBlockStore2) GetReceiptsByHash(hash types.Hash) ([]*types.Receipt, error) {
	return nil, nil
}

func (m *mockBlockStore2) GetHeaderByNumber(blockNumber uint64) (*types.Header, bool) {
	b, ok := m.GetBlockByNumber(blockNumber, false)
	if !ok {
		return nil, false
	}
	return b.Header, true
}

func (m *mockBlockStore2) GetBlockByNumber(blockNumber uint64, full bool) (*types.Block, bool) {
	for _, b := range m.blocks {
		if b.Number() == blockNumber {
			return b, true
		}
	}
	return nil, false
}

func (m *mockBlockStore2) GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool) {
	for _, b := range m.blocks {
		if b.Hash() == hash {
			return b, true
		}
	}
	return nil, false
}

func (m *mockBlockStore2) Header() *types.Header {
	return m.blocks[len(m.blocks)-1].Header
}

func TestEth_Block_GetBlockByNumber(t *testing.T) {
	store := &mockBlockStore2{}
	for i := 0; i < 10; i++ {
		store.add(&types.Block{
			Header: &types.Header{
				Number: uint64(i),
			},
		})
	}

	dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)

	cases := []struct {
		blockNum BlockNumber
		err      bool
	}{
		{LatestBlockNumber, false},
		{EarliestBlockNumber, true},
		{BlockNumber(-50), true},
		{BlockNumber(0), false},
		{BlockNumber(2), false},
		{BlockNumber(50), true},
	}
	for _, c := range cases {
		_, err := dispatcher.endpoints.Eth.GetBlockByNumber(c.blockNum, false)
		if err != nil && !c.err {
			t.Fatal(err)
		}
		if err == nil && c.err {
			t.Fatal("bad")
		}
	}
}

func TestEth_Block_GetBlockByHash(t *testing.T) {
	store := &mockBlockStore2{}
	store.add(&types.Block{
		Header: &types.Header{
			Hash: hash1,
		},
	})

	dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)

	_, err := dispatcher.endpoints.Eth.GetBlockByHash(hash1, false)
	assert.NoError(t, err)

	_, err = dispatcher.endpoints.Eth.GetBlockByHash(hash2, false)
	assert.Error(t, err)
}

func TestEth_Block_BlockNumber(t *testing.T) {
	store := &mockBlockStore2{}
	store.add(&types.Block{
		Header: &types.Header{
			Number: 10,
		},
	})
	dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)

	num, err := dispatcher.endpoints.Eth.BlockNumber()
	assert.NoError(t, err)
	assert.Equal(t, argUintPtr(10), num)
}

func TestEth_Block_GetLogs(t *testing.T) {

	/*
		// Topics we're searching for
		var topics = [][]types.Hash{
			{types.StringToHash("topic 4")},
			{types.StringToHash("topic 5")},
			{types.StringToHash("topic 6")},
		}

		testTable := []struct {
			name          string
			filterOptions *LogFilter
			shouldFail    bool
		}{
			{"Found matching logs",
				&LogFilter{
					fromBlock: 1,
					toBlock:   4,
					Topics:    topics,
				},
				false},
			{"Invalid block range", &LogFilter{
				fromBlock: 10,
				toBlock:   5,
				Topics:    topics,
			}, true},
			{"Reference blocks not found", &LogFilter{
				fromBlock: 10,
				toBlock:   20,
				Topics:    topics,
			}, true},
		}

		// Setup //
		store := newMockBlockStore()

		dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)

		store.getHeaderByNumberCallback = func(blockNumber uint64) (*types.Header, bool) {
			return &types.Header{
				Number:       blockNumber,
				ReceiptsRoot: types.StringToHash(strconv.FormatUint(blockNumber, 10)),
			}, true
		}

		store.getReceiptsByHashCallback = func(hash types.Hash) ([]*types.Receipt, error) {

			switch hash {
			case types.StringToHash(strconv.FormatUint(1, 10)):
				return store.generateReceipts(
					generateReceiptsParams{
						numReceipts: 1,
						numTopics:   1,
						numLogs:     2,
					},
				), nil
			case types.StringToHash(strconv.FormatUint(2, 10)):
				return store.generateReceipts(
					generateReceiptsParams{
						numReceipts: 2,
						numTopics:   5,
						numLogs:     1,
					},
				), nil
			case types.StringToHash(strconv.FormatUint(3, 10)):
				return store.generateReceipts(
					generateReceiptsParams{
						numReceipts: 3,
						numTopics:   10,
						numLogs:     5,
					},
				), nil
			}

			return nil, nil
		}

		for index, testCase := range testTable {

			if index == 2 {
				store.getHeaderByNumberCallback = func(blockNumber uint64) (*types.Header, bool) {
					return nil, false
				}
			}

			t.Run(testCase.name, func(t *testing.T) {
				foundLogs, logError := dispatcher.endpoints.Eth.GetLogs(
					testCase.filterOptions,
				)

				if logError != nil && !testCase.shouldFail {
					// If there is an error, and the test shouldn't fail
					t.Fatalf("Error: %v", logError)
				} else if !testCase.shouldFail {
					assert.Lenf(t, foundLogs, 17, "Invalid number of logs found")
				}
			})
		}
	*/
}

var (
	addr0                = types.Address{0x1}
	uninitializedAddress = types.Address{0x99}
)

func TestEth_State_GetBalance(t *testing.T) {
	store := &mockAccountStore{}

	acct0 := store.AddAccount(addr0)
	acct0.Balance(100)

	dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)

	balance, err := dispatcher.endpoints.Eth.GetBalance(addr0, LatestBlockNumber)
	assert.NoError(t, err)
	assert.Equal(t, balance, argBigPtr(big.NewInt(100)))

	// address not found
	balance, err = dispatcher.endpoints.Eth.GetBalance(addr1, LatestBlockNumber)
	assert.NoError(t, err)
	assert.Equal(t, balance, argUintPtr(0))
}

func TestEth_State_GetTransactionCount(t *testing.T) {
	store := &mockAccountStore{}

	acct0 := store.AddAccount(addr0)
	acct0.Nonce(100)

	dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)

	balance, err := dispatcher.endpoints.Eth.GetTransactionCount(addr0, LatestBlockNumber)
	assert.NoError(t, err)
	assert.Equal(t, balance, argUintPtr(100))

	// address not found
	_, err = dispatcher.endpoints.Eth.GetTransactionCount(addr1, LatestBlockNumber)
	assert.Error(t, err)
}

var (
	code0 = []byte{0x1, 0x2, 0x3}
)

func TestEth_State_GetCode(t *testing.T) {
	store := &mockAccountStore{}

	acct0 := store.AddAccount(addr0)
	acct0.Code(code0)

	dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)

	t.Run("Initialized address", func(t *testing.T) {
		code, err := dispatcher.endpoints.Eth.GetCode(acct0.address, LatestBlockNumber)
		assert.NoError(t, err)
		assert.Equal(t, code, argBytesPtr(code0))
	})

	t.Run("Uninitialized address", func(t *testing.T) {
		code, err := dispatcher.endpoints.Eth.GetCode(uninitializedAddress, LatestBlockNumber)
		assert.NoError(t, err)
		assert.Equal(t, code, "0x")
	})
}

func TestEth_State_GetStorageAt(t *testing.T) {
	store := &mockAccountStore{}

	acct0 := store.AddAccount(addr0)
	acct0.Storage(hash1, hash1)

	dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)
	res, err := dispatcher.endpoints.Eth.GetStorageAt(acct0.address, hash1, LatestBlockNumber)
	assert.NoError(t, err)
	assert.Equal(t, res, argBytesPtr(hash1.Bytes()))

	// slot not found
	_, err = dispatcher.endpoints.Eth.GetStorageAt(acct0.address, hash2, LatestBlockNumber)
	assert.Error(t, err)
}

type mockStoreTxn struct {
	nullBlockchainInterface

	txn *types.Transaction
}

func (m *mockStoreTxn) AddTx(tx *types.Transaction) error {
	m.txn = tx
	return nil
}

func (m *mockStoreTxn) GetNonce(addr types.Address) (uint64, bool) {
	return 1, false
}

func TestEth_TxnPool_SendRawTransaction(t *testing.T) {
	store := &mockStoreTxn{}
	dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)

	txn := &types.Transaction{
		From: addr0,
		V:    1,
	}
	txn.ComputeHash()

	data := txn.MarshalRLP()
	_, err := dispatcher.endpoints.Eth.SendRawTransaction(hex.EncodeToHex(data))
	assert.NoError(t, err)
	assert.NotEqual(t, store.txn.Hash, types.ZeroHash)

	// the hash in the txn pool should match the one we send
	if txn.Hash != store.txn.Hash {
		t.Fatal("bad")
	}
}

func TestEth_TxnPool_SendTransaction(t *testing.T) {
	store := &mockStoreTxn{}
	dispatcher := newTestDispatcher(hclog.NewNullLogger(), store)

	arg := &txnArgs{
		From:     argAddrPtr(addr0),
		To:       argAddrPtr(addr0),
		Nonce:    argUintPtr(0),
		GasPrice: argBytesPtr([]byte{0x1}),
	}
	_, err := dispatcher.endpoints.Eth.SendTransaction(arg)
	assert.NoError(t, err)
	assert.NotEqual(t, store.txn.Hash, types.ZeroHash)
}
