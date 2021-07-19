package txpool

import (
	"fmt"
	"math/big"
	"strconv"
	"testing"

	"github.com/0xPolygon/minimal/crypto"
	"github.com/0xPolygon/minimal/network"
	"github.com/0xPolygon/minimal/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
)

func TestMultipleTransactions(t *testing.T) {
	// if we add the same transaction it should only be included once
	pool, err := NewTxPool(hclog.NewNullLogger(), false, &mockStore{}, nil, nil)
	assert.NoError(t, err)
	pool.EnableDev()

	from1 := types.Address{0x1}

	txn0 := &types.Transaction{
		From:     from1,
		Nonce:    10,
		GasPrice: big.NewInt(1),
	}
	assert.NoError(t, pool.addImpl("", txn0))
	assert.NoError(t, pool.addImpl("", txn0))

	assert.Len(t, pool.queue[from1].txs, 1)
	assert.Equal(t, pool.Length(), uint64(0))

	from2 := types.Address{0x2}
	txn1 := &types.Transaction{
		From:     from2,
		GasPrice: big.NewInt(1),
	}
	assert.NoError(t, pool.addImpl("", txn1))
	assert.NoError(t, pool.addImpl("", txn1))

	assert.Len(t, pool.queue[from2].txs, 0)
	assert.Equal(t, pool.Length(), uint64(1))
}

func TestBroadcast(t *testing.T) {
	// we need a fully encrypted txn with (r, s, v) values so that we can
	// safely encrypt in RLP and broadcast it
	key0, _ := crypto.GenerateKey()
	addr0 := crypto.PubKeyToAddress(&key0.PublicKey)

	fmt.Println("-- addr")
	fmt.Println(addr0)

	signer := &crypto.FrontierSigner{}

	createPool := func() *TxPool {
		pool, err := NewTxPool(hclog.NewNullLogger(), false, &mockStore{}, nil, network.CreateServer(t, nil))
		assert.NoError(t, err)
		pool.AddSigner(signer)
		return pool
	}

	pool1 := createPool()
	pool2 := createPool()

	network.MultiJoin(t, pool1.network, pool2.network)

	// broadcast txn1 from pool1
	txn1 := &types.Transaction{
		Value:    big.NewInt(10),
		GasPrice: big.NewInt(1),
	}

	txn1, err := signer.SignTx(txn1, key0)
	assert.NoError(t, err)

	assert.NoError(t, pool1.AddTx(txn1))
	fmt.Println(pool1.Length())
}

type mockStore struct {
}

func (m *mockStore) GetNonce(root types.Hash, addr types.Address) uint64 {
	return 0
}

func (m *mockStore) GetBlockByHash(types.Hash, bool) (*types.Block, bool) {
	return nil, false
}

func (m *mockStore) Header() *types.Header {
	return &types.Header{}
}

func TestTxnQueue_Promotion(t *testing.T) {
	pool, err := NewTxPool(hclog.NewNullLogger(), false, &mockStore{}, nil, nil)
	assert.NoError(t, err)
	pool.EnableDev()

	addr1 := types.Address{0x1}

	pool.addImpl("", &types.Transaction{
		From:     addr1,
		GasPrice: big.NewInt(1),
	})

	nonce, _ := pool.GetNonce(addr1)
	assert.Equal(t, nonce, uint64(1))

	// though txn0 is not being processed yet and the current nonce is 0
	// we need to consider that txn0 is on the sorted pool so this one is promoted too
	pool.addImpl("", &types.Transaction{
		From:     addr1,
		Nonce:    1,
		GasPrice: big.NewInt(1),
	})

	nonce, _ = pool.GetNonce(addr1)
	assert.Equal(t, nonce, uint64(2))
	assert.Equal(t, pool.Length(), uint64(2))
}

func TestTxnQueue_Heap(t *testing.T) {
	type TestCase struct {
		From     types.Address
		GasPrice *big.Int
		Nonce    uint64
		Index    int
	}

	addr1 := types.Address{0x1}
	addr2 := types.Address{0x2}

	test := func(t *testing.T, testTable []TestCase) {
		pool, err := NewTxPool(hclog.NewNullLogger(), false, &mockStore{}, nil, nil)
		assert.NoError(t, err)
		pool.EnableDev()

		for _, testCase := range testTable {
			err := pool.addImpl("", &types.Transaction{
				From:     testCase.From,
				GasPrice: testCase.GasPrice,
				Nonce:    testCase.Nonce,
			})
			assert.NoError(t, err)
		}

		for _, testCase := range testTable {
			transaction, _ := pool.Pop()

			assert.NotNil(t, transaction)

			actual := TestCase{
				From:     transaction.From,
				GasPrice: transaction.GasPrice,
				Nonce:    transaction.Nonce,
			}

			assert.EqualValues(t, testCase, actual)
		}

		empty, _ := pool.Pop()
		assert.Nil(t, empty)
	}

	t.Run("the higher priced transaction should be popped first", func(t *testing.T) {
		test(t, []TestCase{
			{
				From:     addr1,
				GasPrice: big.NewInt(2),
			},
			{
				From:     addr2,
				GasPrice: big.NewInt(1),
			},
		})

	})

	t.Run("sort by nonce when same from address", func(t *testing.T) {
		test(t, []TestCase{
			{
				From:     addr1,
				GasPrice: big.NewInt(2),
				Nonce:    0,
			},
			{
				From:     addr1,
				GasPrice: big.NewInt(1),
				Nonce:    1,
			},
		})
	})

	t.Run("make sure that heap is not functioning as a FIFO", func(t *testing.T) {
		pool, err := NewTxPool(hclog.NewNullLogger(), false, &mockStore{}, nil, nil)
		assert.NoError(t, err)
		pool.EnableDev()

		numTxns := 5
		txns := make([]*types.Transaction, numTxns)

		for i := 0; i < numTxns; i++ {
			txns[i] = &types.Transaction{
				From:     types.StringToAddress(strconv.Itoa(i + 1)),
				GasPrice: big.NewInt(int64(i + 1)),
			}

			addErr := pool.addImpl("", txns[i])
			assert.Nilf(t, addErr, "Unable to add transaction to pool")
		}

		for i := numTxns - 1; i >= 0; i-- {
			txn, _ := pool.Pop()
			assert.Equalf(t, txns[i].GasPrice, txn.GasPrice, "Expected output mismatch")
		}
	})
}
