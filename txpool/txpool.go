package txpool

import (
	"container/heap"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/0xPolygon/minimal/blockchain"
	"github.com/0xPolygon/minimal/network"
	"github.com/0xPolygon/minimal/txpool/proto"
	"github.com/0xPolygon/minimal/types"
	"github.com/golang/protobuf/ptypes/any"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc"
)

const (
	defaultIdlePeriod = 1 * time.Minute
)

type store interface {
	Header() *types.Header
	GetNonce(root types.Hash, addr types.Address) uint64
	GetBlockByHash(types.Hash, bool) (*types.Block, bool)
}

type signer interface {
	Sender(tx *types.Transaction) (types.Address, error)
}

// TxPool is a pool of transactions
type TxPool struct {
	logger hclog.Logger
	signer signer

	store      store
	idlePeriod time.Duration

	// unsorted list of transactions per account
	queue map[types.Address]*txQueue

	// sorted list of current valid transactions
	sorted *txPriceHeap

	// network stack
	network *network.Server
	topic   *network.Topic

	sealing  bool
	dev      bool
	NotifyCh chan struct{}

	proto.UnimplementedTxnPoolOperatorServer
}

// NewTxPool creates a new pool for transactions
func NewTxPool(
	logger hclog.Logger,
	sealing bool,
	store store,
	grpcServer *grpc.Server,
	network *network.Server,
) (*TxPool, error) {
	txPool := &TxPool{
		logger:     logger.Named("txpool"),
		store:      store,
		idlePeriod: defaultIdlePeriod,
		queue:      make(map[types.Address]*txQueue),
		network:    network,
		sorted:     newTxPriceHeap(),
		sealing:    sealing,
	}

	if network != nil {
		// subscribe to the gossip protocol
		topic, err := network.NewTopic(topicNameV1, &proto.Txn{})
		if err != nil {
			return nil, err
		}
		topic.Subscribe(txPool.handleGossipTxn)
		txPool.topic = topic
	}

	if grpcServer != nil {
		proto.RegisterTxnPoolOperatorServer(grpcServer, txPool)
	}
	return txPool, nil
}

func (t *TxPool) GetNonce(addr types.Address) (uint64, bool) {
	q, ok := t.queue[addr]
	if !ok {
		return 0, false
	}
	return q.nextNonce, true
}

func (t *TxPool) AddSigner(s signer) {
	// TODO: We can add more types of signers here
	t.signer = s
}

var topicNameV1 = "txpool/0.1"

func (t *TxPool) handleGossipTxn(obj interface{}) {
	if !t.sealing {
		return
	}

	raw := obj.(*proto.Txn)
	txn := new(types.Transaction)
	if err := txn.UnmarshalRLP(raw.Raw.Value); err != nil {
		t.logger.Error("failed to decode broadcasted txn", "err", err)
	} else {
		if err := t.addImpl("gossip", txn); err != nil {
			t.logger.Error("failed to add broadcasted txn", "err", err)
		}
	}
}

func (t *TxPool) EnableDev() {
	t.dev = true
}

// AddTx adds a new transaction to the pool
func (t *TxPool) AddTx(tx *types.Transaction) error {
	if err := t.addImpl("addTxn", tx); err != nil {
		return err
	}

	// broadcast the transaction only if network is enabled
	// and we are not in dev mode
	if t.topic != nil && !t.dev {
		txn := &proto.Txn{
			Raw: &any.Any{
				Value: tx.MarshalRLP(),
			},
		}
		if err := t.topic.Publish(txn); err != nil {
			t.logger.Error("failed to topic txn", "err", err)
		}
	}

	if t.NotifyCh != nil {
		select {
		case t.NotifyCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (t *TxPool) addImpl(ctx string, txns ...*types.Transaction) error {
	if len(txns) == 0 {
		return nil
	}

	from := txns[0].From
	for _, txn := range txns {
		// Since this is a single point of inclusion for new transactions both
		// to the promoted queue and pending queue we use this point to calculate the hash
		txn.ComputeHash()

		err := t.validateTx(txn)
		if err != nil {
			return err
		}

		if txn.From == types.ZeroAddress {
			txn.From, err = t.signer.Sender(txn)
			if err != nil {
				return fmt.Errorf("invalid sender")
			}
			from = txn.From
		} else {
			// only if we are in dev mode we can accept
			// a transaction without validation
			if !t.dev {
				return fmt.Errorf("cannot accept non-encrypted txn")
			}
		}

		t.logger.Debug("add txn", "ctx", ctx, "hash", txn.Hash, "from", from)
	}

	txnsQueue, ok := t.queue[from]
	if !ok {
		stateRoot := t.store.Header().StateRoot

		// initialize the txn queue for the account
		txnsQueue = newTxQueue()
		txnsQueue.nextNonce = t.store.GetNonce(stateRoot, from)
		t.queue[from] = txnsQueue
	}
	for _, txn := range txns {
		txnsQueue.Add(txn)
	}

	for _, promoted := range txnsQueue.Promote() {
		t.sorted.Push(promoted)
	}
	return nil
}

func (t *TxPool) Length() uint64 {
	return t.sorted.Length()
}

func (t *TxPool) Pop() (*types.Transaction, func()) {
	txn := t.sorted.Pop()
	if txn == nil {
		return nil, nil
	}
	ret := func() {
		t.sorted.Push(txn.tx)
	}
	return txn.tx, ret
}

func (t *TxPool) ResetWithHeader(h *types.Header) {
	evnt := &blockchain.Event{
		NewChain: []*types.Header{h},
	}
	t.ProcessEvent(evnt)
}

// ProcessEvent processes the blockchain event and resets the txpool accordingly
func (t *TxPool) ProcessEvent(evnt *blockchain.Event) {
	addTxns := map[types.Hash]*types.Transaction{}
	for _, evnt := range evnt.OldChain {
		// reinject these transactions on the pool
		block, ok := t.store.GetBlockByHash(evnt.Hash, true)
		if !ok {
			t.logger.Error("block not found on txn add", "hash", block.Hash())
		} else {
			for _, txn := range block.Transactions {
				addTxns[txn.Hash] = txn
			}
		}
	}

	delTxns := map[types.Hash]*types.Transaction{}
	for _, evnt := range evnt.NewChain {
		// remove these transactions from the pool
		block, ok := t.store.GetBlockByHash(evnt.Hash, true)
		if !ok {
			t.logger.Error("block not found on txn del", "hash", block.Hash())
		} else {
			for _, txn := range block.Transactions {
				delete(addTxns, txn.Hash)
				delTxns[txn.Hash] = txn
			}
		}
	}

	// try to include again the transactions in the sorted list
	for _, txn := range addTxns {
		if err := t.addImpl("reorg", txn); err != nil {
			t.logger.Error("failed to add txn", "err", err)
		}
	}

	// remove the mined transactions from the sorted list
	for _, txn := range delTxns {
		t.sorted.Delete(txn)
	}
}

func (t *TxPool) validateTx(tx *types.Transaction) error {
	/*
		if tx.Size() > 32*1024 {
			return fmt.Errorf("oversize data")
		}
		if tx.Value.Sign() < 0 {
			return fmt.Errorf("negative value")
		}
	*/
	return nil
}

type txQueue struct {
	txs       txHeap
	nextNonce uint64
}

func newTxQueue() *txQueue {
	return &txQueue{
		txs: txHeap{},
	}
}

func (t *txQueue) Reset(txns ...*types.Transaction) {
	lowestNonce := t.nextNonce
	for _, txn := range txns {
		if txn.Nonce < lowestNonce {
			lowestNonce = txn.Nonce
		}
		t.Push(txn)
	}
	t.nextNonce = lowestNonce
}

// Add adds a new tx into the queue
func (t *txQueue) Add(tx *types.Transaction) {
	t.Push(tx)
}

// Promote promotes all the new valid transactions
func (t *txQueue) Promote() []*types.Transaction {
	// Remove elements lower than nonce
	for {
		tx := t.Peek()
		if tx == nil || tx.Nonce >= t.nextNonce {
			break
		}
		t.Pop()
	}

	// Promote elements
	tx := t.Peek()
	if tx == nil || tx.Nonce != t.nextNonce {
		return nil
	}

	promote := []*types.Transaction{}
	for {
		promote = append(promote, tx)
		t.Pop()

		tx2 := t.Peek()
		if tx2 == nil || tx.Nonce+1 != tx2.Nonce {
			break
		}
		tx = tx2
	}
	if len(promote) == 0 {
		return nil
	}

	lastTxn := promote[len(promote)-1]
	t.nextNonce = lastTxn.Nonce + 1

	return promote
}

func (t *txQueue) Peek() *types.Transaction {
	return t.txs.Peek()
}

func (t *txQueue) Push(tx *types.Transaction) {
	// try to find the txn in the set
	i := sort.Search(len(t.txs), func(i int) bool {
		return t.txs[0].Nonce >= tx.Nonce
	})
	if i < len(t.txs) && t.txs[i].Nonce == tx.Nonce {
		// txns with the same nonce is on the list
		return
	}

	heap.Push(&t.txs, tx)
}

func (t *txQueue) Pop() *types.Transaction {
	res := heap.Pop(&t.txs)
	if res == nil {
		return nil
	}

	return res.(*types.Transaction)
}

// Nonce ordered heap

type txHeap []*types.Transaction

func (t *txHeap) Peek() *types.Transaction {
	if len(*t) == 0 {
		return nil
	}
	return (*t)[0]
}

func (t *txHeap) Len() int {
	return len(*t)
}

func (t *txHeap) Swap(i, j int) {
	(*t)[i], (*t)[j] = (*t)[j], (*t)[i]
}

func (t *txHeap) Less(i, j int) bool {
	return (*t)[i].Nonce < (*t)[j].Nonce
}

func (t *txHeap) Push(x interface{}) {
	(*t) = append((*t), x.(*types.Transaction))
}

func (t *txHeap) Pop() interface{} {
	old := *t
	n := len(old)
	x := old[n-1]
	*t = old[0 : n-1]
	return x
}

// Price ordered heap

type pricedTx struct {
	tx    *types.Transaction
	from  types.Address
	price *big.Int
	index int
}

type txPriceHeap struct {
	lock  sync.Mutex
	index map[types.Hash]*pricedTx
	heap  txPriceHeapImpl
}

func newTxPriceHeap() *txPriceHeap {
	return &txPriceHeap{
		index: make(map[types.Hash]*pricedTx),
		heap:  make(txPriceHeapImpl, 0),
	}
}

func (t *txPriceHeap) Length() uint64 {
	t.lock.Lock()
	defer t.lock.Unlock()

	return uint64(len(t.index))
}

func (t *txPriceHeap) Delete(tx *types.Transaction) {
	t.lock.Lock()
	defer t.lock.Unlock()

	if item, ok := t.index[tx.Hash]; ok {
		heap.Remove(&t.heap, item.index)
		delete(t.index, tx.Hash)
	}
}

func (t *txPriceHeap) Push(tx *types.Transaction) error {
	t.lock.Lock()
	defer t.lock.Unlock()

	price := new(big.Int).Set(tx.GasPrice)

	if _, ok := t.index[tx.Hash]; ok {
		return fmt.Errorf("tx %s already exists", tx.Hash)
	}

	pTx := &pricedTx{
		tx:    tx,
		from:  tx.From,
		price: price,
	}
	t.index[tx.Hash] = pTx
	heap.Push(&t.heap, pTx)
	return nil
}

func (t *txPriceHeap) Pop() *pricedTx {
	t.lock.Lock()
	defer t.lock.Unlock()

	if len(t.index) == 0 {
		return nil
	}
	tx := heap.Pop(&t.heap).(*pricedTx)
	delete(t.index, tx.tx.Hash)
	return tx
}

func (t *txPriceHeap) Contains(tx *types.Transaction) bool {
	_, ok := t.index[tx.Hash]
	return ok
}

type txPriceHeapImpl []*pricedTx

func (t txPriceHeapImpl) Len() int { return len(t) }

func (t txPriceHeapImpl) Less(i, j int) bool {
	if t[i].from == t[j].from {
		return t[i].tx.Nonce < t[j].tx.Nonce
	}

	return t[i].price.Cmp(t[j].price) >= 0
}

func (t txPriceHeapImpl) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
	t[i].index = i
	t[j].index = j
}

func (t *txPriceHeapImpl) Push(x interface{}) {
	n := len(*t)
	job := x.(*pricedTx)
	job.index = n
	*t = append(*t, job)
}

func (t *txPriceHeapImpl) Pop() interface{} {
	old := *t
	n := len(old)
	job := old[n-1]
	job.index = -1
	*t = old[0 : n-1]
	return job
}
