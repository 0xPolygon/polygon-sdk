package state

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/0xPolygon/minimal/staking"
	"github.com/0xPolygon/minimal/state/runtime/system"
	"github.com/0xPolygon/minimal/types"

	"github.com/0xPolygon/minimal/chain"
	"github.com/0xPolygon/minimal/crypto"
	"github.com/0xPolygon/minimal/state/runtime"
)

const (
	spuriousDragonMaxCodeSize = 24576
)

var (
	errorVMOutOfGas = fmt.Errorf("out of gas")
)

var emptyCodeHashTwo = types.BytesToHash(crypto.Keccak256(nil))

// GetHashByNumber returns the hash function of a block number
type GetHashByNumber = func(i uint64) types.Hash

type GetHashByNumberHelper = func(*types.Header) GetHashByNumber

type StakingEventType string

const (
	StakingEventStaked   StakingEventType = "Staked"
	StakingEventUnstaked StakingEventType = "Unstaked"
)

type StakingEvent struct {
	Number  int64
	Type    StakingEventType
	Address types.Address
	Amount  *big.Int
}

type StakingEventSubscription struct {
	eventCh chan *StakingEvent
	wg      *sync.WaitGroup
}

func (s *StakingEventSubscription) EventCh() <-chan *StakingEvent {
	return s.eventCh
}

func (s *StakingEventSubscription) Close() {
	if s.wg != nil {
		s.wg.Wait()
	}
	close(s.eventCh)
}

// Executor is the main entity
type Executor struct {
	config   *chain.Params
	runtimes []runtime.Runtime
	state    State
	GetHash  GetHashByNumberHelper

	PostHook func(txn *Transition)

	stakingEventSubscriptions     []*StakingEventSubscription
	stakingEventSubscriptionsLock sync.Mutex
}

// NewExecutor creates a new executor
func NewExecutor(config *chain.Params, s State) *Executor {
	return &Executor{
		config:                        config,
		runtimes:                      []runtime.Runtime{},
		state:                         s,
		stakingEventSubscriptions:     []*StakingEventSubscription{},
		stakingEventSubscriptionsLock: sync.Mutex{},
	}
}

func (e *Executor) WriteGenesis(
	alloc map[types.Address]*chain.GenesisAccount,
	allocStake map[types.Address]*chain.GenesisStake,
) types.Hash {
	snap := e.state.NewSnapshot()
	txn := NewTxn(e.state, snap)

	stakingAddress := types.StringToAddress(system.StakingAddress)

	for addr, account := range alloc {
		if account.Balance != nil {
			txn.AddBalance(addr, account.Balance)
		}
		if account.Nonce != 0 {
			txn.SetNonce(addr, account.Nonce)
		}
		if len(account.Code) != 0 {
			txn.SetCode(addr, account.Code)
		}
		for key, value := range account.Storage {
			txn.SetState(addr, key, value)
		}
	}

	hub := staking.GetStakingHub()
	for addr, stake := range allocStake {
		hub.IncreaseStake(addr, stake.StakedBalance)

		txn.AddBalance(stakingAddress, stake.StakedBalance)
	}

	_, root := txn.Commit(false)
	return types.BytesToHash(root)
}

// SetRuntime adds a runtime to the runtime set
func (e *Executor) SetRuntime(r runtime.Runtime) {
	e.runtimes = append(e.runtimes, r)
}

type BlockResult struct {
	Root     types.Hash
	Receipts []*types.Receipt
	TotalGas uint64
}

// getStakingEventType is a helper method for getting the type of system event
func getStakingEventType(toAddress types.Address) staking.StakingEventType {
	if toAddress == types.StringToAddress(system.StakingAddress) {
		return staking.StakingEvent
	}

	return staking.UnstakingEvent
}

// constructPendingEvent is a helper method for constructing a pending event
func constructPendingEvent(
	toAddress types.Address,
	fromAddress types.Address,
	value *big.Int,
) staking.PendingEvent {
	eventType := getStakingEventType(toAddress)

	return staking.PendingEvent{
		Address:   fromAddress,
		Value:     value,
		EventType: eventType,
	}
}

// cleanDiscardedEvents is a helper method for clearing discarded pending events
func cleanDiscardedEvents(transactions []*types.Transaction) {
	for _, discardedTxn := range transactions {
		// Remove a pending event for the discarded transaction
		if system.IsSystemEvent(discardedTxn.To) {
			pendingEvent := constructPendingEvent(
				*discardedTxn.To,
				discardedTxn.From,
				discardedTxn.Value,
			)

			hub := staking.GetStakingHub()
			if hub.ContainsPendingEvent(pendingEvent) {
				// Remove the event from the queue
				hub.RemovePendingEvent(pendingEvent)
			}
		}
	}
}

// commitApprovedEvents is a helper method for committing pending events that passed all checks
func commitApprovedEvents(transactions []*types.Transaction) {
	for _, t := range transactions {
		// Check if the transaction matches a staking / unstaking event
		if system.IsSystemEvent(t.To) {
			pendingEvent := constructPendingEvent(*t.To, t.From, t.Value)

			hub := staking.GetStakingHub()

			if hub.ContainsPendingEvent(pendingEvent) {
				// Remove the event from the queue
				hub.RemovePendingEvent(pendingEvent)

				// Execute the changes on the staking map
				if pendingEvent.EventType == staking.StakingEvent {
					hub.IncreaseStake(t.From, t.Value)
				} else {
					hub.ResetStake(t.From)
				}
			}
		}
	}
}

// ProcessBlock already does all the handling of the whole process, TODO
func (e *Executor) ProcessBlock(parentRoot types.Hash, block *types.Block, blockCreator types.Address) (*BlockResult, error) {
	txn, err := e.BeginTxn(parentRoot, block.Header, blockCreator)
	if err != nil {
		return nil, err
	}

	txn.block = block
	for _, t := range block.Transactions {
		if err := txn.Write(t); err != nil {
			// Because block processing termination is here,
			// the executor needs to handle any "stale" pending events,
			// since every transaction in this block is discarded
			cleanDiscardedEvents(block.Transactions)

			return nil, err
		}
	}
	_, root := txn.Commit()

	// Checks are passed, do staking logic if possible
	commitApprovedEvents(block.Transactions)

	res := &BlockResult{
		Root:     root,
		Receipts: txn.Receipts(),
		TotalGas: txn.TotalGas(),
	}
	return res, nil
}

// StateAt returns snapshot at given root
func (e *Executor) State() State {
	return e.state
}

// StateAt returns snapshot at given root
func (e *Executor) StateAt(root types.Hash) (Snapshot, error) {
	return e.state.NewSnapshotAt(root)
}

// SubscribeStakingEvent subscribes staking event at given number
func (e *Executor) SubscribeStakingEvent() *StakingEventSubscription {
	e.stakingEventSubscriptionsLock.Lock()
	defer e.stakingEventSubscriptionsLock.Unlock()
	sub := &StakingEventSubscription{
		eventCh: make(chan *StakingEvent),
		wg:      &sync.WaitGroup{},
	}
	e.stakingEventSubscriptions = append(e.stakingEventSubscriptions, sub)
	return sub
}

// UnsubscribeStakingEvent removes given subscription from list
func (e *Executor) UnsubscribeStakingEvent(target *StakingEventSubscription) {
	e.stakingEventSubscriptionsLock.Lock()
	defer e.stakingEventSubscriptionsLock.Unlock()
	newSubs := []*StakingEventSubscription{}
	for _, s := range e.stakingEventSubscriptions {
		if s != target {
			newSubs = append(newSubs, s)
		}
	}
	e.stakingEventSubscriptions = newSubs
}

// publishStakingEvent publishes a staking event to subscriptions
func (e *Executor) publishStakingEvent(event *StakingEvent) {
	e.stakingEventSubscriptionsLock.Lock()
	defer e.stakingEventSubscriptionsLock.Unlock()
	for _, sub := range e.stakingEventSubscriptions {
		sub.wg.Add(1)
		go func(sub *StakingEventSubscription) {
			sub.eventCh <- event
			sub.wg.Done()
		}(sub)
	}
}

func (e *Executor) BeginTxn(parentRoot types.Hash, header *types.Header, coinbaseReceiver types.Address) (*Transition, error) {
	config := e.config.Forks.At(header.Number)

	auxSnap2, err := e.state.NewSnapshotAt(parentRoot)
	if err != nil {
		return nil, err
	}

	newTxn := NewTxn(e.state, auxSnap2)

	env2 := runtime.TxContext{
		Coinbase:   coinbaseReceiver,
		Timestamp:  int64(header.Timestamp),
		Number:     int64(header.Number),
		Difficulty: types.BytesToHash(new(big.Int).SetUint64(header.Difficulty).Bytes()),
		GasLimit:   int64(header.GasLimit),
		ChainID:    int64(e.config.ChainID),
	}

	txn := &Transition{
		r:        e,
		ctx:      env2,
		state:    newTxn,
		getHash:  e.GetHash(header),
		auxState: e.state,
		config:   config,
		gasPool:  uint64(env2.GasLimit),

		receipts: []*types.Receipt{},
		totalGas: 0,
	}
	return txn, nil
}

type Transition struct {
	// dummy
	auxState State

	// the current block being processed
	block *types.Block

	r       *Executor
	config  chain.ForksInTime
	state   *Txn
	getHash GetHashByNumber
	ctx     runtime.TxContext
	gasPool uint64

	// result
	receipts []*types.Receipt
	totalGas uint64

	// The return value for the contract execution
	returnValue []byte
}

func (t *Transition) ReturnValue() []byte {
	return t.returnValue
}

func (t *Transition) TotalGas() uint64 {
	return t.totalGas
}

func (t *Transition) Receipts() []*types.Receipt {
	return t.receipts
}

var emptyFrom = types.Address{}

// Write writes another transaction to the executor
func (t *Transition) Write(txn *types.Transaction) error {
	signer := crypto.NewSigner(t.config, uint64(t.r.config.ChainID))

	var err error
	if txn.From == emptyFrom {
		// Decrypt the from address
		txn.From, err = signer.Sender(txn)
		if err != nil {
			return err
		}
	}

	// Make a local copy and apply the transaction
	msg := txn.Copy()

	gasUsed, failed, err := t.Apply(msg)
	if err != nil {
		fmt.Printf("Apply err: %v", err)
	}
	t.totalGas += gasUsed

	logs := t.state.Logs()

	var root []byte

	receipt := &types.Receipt{
		CumulativeGasUsed: t.totalGas,
		TxHash:            txn.Hash,
		GasUsed:           gasUsed,
	}

	if t.config.Byzantium {
		// The suicided accounts are set as deleted for the next iteration
		t.state.CleanDeleteObjects(true)

		if failed {
			receipt.SetStatus(types.ReceiptFailed)
		} else {
			receipt.SetStatus(types.ReceiptSuccess)
		}

	} else {
		ss, aux := t.state.Commit(t.config.EIP155)
		t.state = NewTxn(t.auxState, ss)
		root = aux
		receipt.Root = types.BytesToHash(root)
	}

	// if the transaction created a contract, store the creation address in the receipt.
	if msg.To == nil {
		receipt.ContractAddress = crypto.CreateAddress(msg.From, txn.Nonce)
	}

	// Set the receipt logs and create a bloom for filtering
	receipt.Logs = logs
	receipt.LogsBloom = types.CreateBloom([]*types.Receipt{receipt})
	t.receipts = append(t.receipts, receipt)

	return nil
}

// Commit commits the final result
func (t *Transition) Commit() (Snapshot, types.Hash) {
	s2, root := t.state.Commit(t.config.EIP155)

	return s2, types.BytesToHash(root)
}

func (t *Transition) subGasPool(amount uint64) error {
	if t.gasPool < amount {
		return fmt.Errorf("gas limit reached in the pool")
	}
	t.gasPool -= amount
	return nil
}

func (t *Transition) addGasPool(amount uint64) {
	t.gasPool += amount
}

func (t *Transition) SetTxn(txn *Txn) {
	t.state = txn
}

func (t *Transition) Txn() *Txn {
	return t.state
}

func (t *Transition) GetTxnHash() types.Hash {
	return t.block.Hash()
}

// Apply applies a new transaction
func (t *Transition) Apply(msg *types.Transaction) (uint64, bool, error) {
	s := t.state.Snapshot()
	returnValue, gasUsed, failed, err := t.apply(msg)
	if err != nil {
		t.state.RevertToSnapshot(s)
	}

	if t.r.PostHook != nil {
		t.r.PostHook(t)
	}

	t.returnValue = returnValue
	return gasUsed, failed, err
}

// ContextPtr returns reference of context
// This method is called only by test
func (t *Transition) ContextPtr() *runtime.TxContext {
	return &t.ctx
}

func (t *Transition) transactionGasCost(msg *types.Transaction) uint64 {
	cost := uint64(0)

	// Contract creation is only paid on the homestead fork
	if msg.IsContractCreation() && t.config.Homestead {
		cost += 53000
	} else {
		cost += 21000
	}

	payload := msg.Input
	if len(payload) > 0 {
		zeros := 0
		for i := 0; i < len(payload); i++ {
			if payload[i] == 0 {
				zeros++
			}
		}
		nonZeros := len(payload) - zeros
		cost += uint64(zeros) * 4

		nonZeroCost := uint64(68)
		if t.config.Istanbul {
			nonZeroCost = 16
		}
		cost += uint64(nonZeros) * nonZeroCost
	}

	return uint64(cost)
}

func (t *Transition) preCheck(msg *types.Transaction) (uint64, error) {
	// validate nonce
	nonce := t.state.GetNonce(msg.From)
	if nonce < msg.Nonce {
		return 0, fmt.Errorf("nonce is too big: %d < %d", nonce, msg.Nonce)
	} else if nonce > msg.Nonce {
		return 0, fmt.Errorf("nonce is too low: %d > %d", nonce, msg.Nonce)
	}

	// deduct the upfront max gas cost
	upfrontGasCost := new(big.Int).Set(msg.GasPrice)
	// TODO: No need to reassign upfrontGasCost on the left
	upfrontGasCost = upfrontGasCost.Mul(upfrontGasCost, new(big.Int).SetUint64(msg.Gas))
	balance := t.state.GetBalance(msg.From)

	// TODO: Remove if, let SubBalance return error
	if balance.Cmp(upfrontGasCost) < 0 {
		return 0, fmt.Errorf("balance %s not enough to pay gas %s", balance, upfrontGasCost)
	}
	t.state.SubBalance(msg.From, upfrontGasCost)

	// calculate gas available for the transaction
	intrinsicGas := t.transactionGasCost(msg)
	gasAvailable := msg.Gas - intrinsicGas

	return gasAvailable, nil
}

func (t *Transition) apply(msg *types.Transaction) (
	[]byte, uint64, bool, error,
) {
	// check if there is enough gas in the pool
	if err := t.subGasPool(msg.Gas); err != nil {
		return nil, 0, false, err
	}

	txn := t.state

	leftoverGas, err := t.preCheck(msg)
	if err != nil {
		return nil, 0, false, err
	}
	// TODO: Check if this is even possible
	if leftoverGas > msg.Gas {
		return nil, 0, false, errorVMOutOfGas
	}

	gasPrice := new(big.Int).Set(msg.GasPrice)
	value := new(big.Int).Set(msg.Value)

	// Set the specific transaction fields in the context
	t.ctx.GasPrice = types.BytesToHash(gasPrice.Bytes())
	t.ctx.Origin = msg.From

	var subErr error
	var gasLeft uint64
	var returnValue []byte

	if msg.IsContractCreation() {
		_, gasLeft, subErr = t.Create2(msg.From, msg.Input, value, leftoverGas)
	} else {
		txn.IncrNonce(msg.From)
		returnValue, gasLeft, subErr = t.Call2(msg.From, *msg.To, msg.Input, value, leftoverGas)
	}
	if subErr != nil {
		// fmt.Printf("suberr: %s\n", subErr.Error())

		if subErr == runtime.ErrNotEnoughFunds {
			return nil, 0, false, subErr
		}
	}

	gasUsed := msg.Gas - gasLeft
	refund := gasUsed / 2
	// Refund can go up to half the gas used
	if refund > txn.GetRefund() {
		refund = txn.GetRefund()
	}

	gasLeft += refund
	gasUsed -= refund

	// refund the sender
	remaining := new(big.Int).Mul(new(big.Int).SetUint64(gasLeft), gasPrice)
	txn.AddBalance(msg.From, remaining)

	// pay the coinbase
	coinbaseFee := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), gasPrice)
	txn.AddBalance(t.ctx.Coinbase, coinbaseFee)

	// return gas to the pool
	t.addGasPool(gasLeft)

	return returnValue, gasUsed, subErr != nil, nil
}

func (t *Transition) Create2(caller types.Address, code []byte, value *big.Int, gas uint64) ([]byte, uint64, error) {
	address := crypto.CreateAddress(caller, t.state.GetNonce(caller))
	contract := runtime.NewContractCreation(1, caller, caller, address, value, gas, code)
	return t.applyCreate(contract, t)
}

func (t *Transition) Call2(caller types.Address, to types.Address, input []byte, value *big.Int, gas uint64) ([]byte, uint64, error) {
	c := runtime.NewContractCall(1, caller, caller, to, value, gas, t.state.GetCode(to), input)
	return t.applyCall(c, runtime.Call, t)
}

func (t *Transition) run(contract *runtime.Contract, host runtime.Host) ([]byte, uint64, error) {
	for _, r := range t.r.runtimes {
		if r.CanRun(contract, host, &t.config) {
			return r.Run(contract, host, &t.config)
		}
	}
	return nil, 0, fmt.Errorf("not found")
}

func (t *Transition) transfer(from, to types.Address, amount *big.Int) error {
	if amount == nil {
		return nil
	}

	balance := t.state.GetBalance(from)
	if balance.Cmp(amount) < 0 {
		return runtime.ErrNotEnoughFunds
	}

	t.state.SubBalance(from, amount)
	t.state.AddBalance(to, amount)
	return nil
}

func (t *Transition) applyCall(c *runtime.Contract, callType runtime.CallType, host runtime.Host) ([]byte, uint64, error) {
	if c.Depth > int(1024)+1 {
		return nil, c.Gas, runtime.ErrDepth
	}

	snapshot := t.state.Snapshot()
	t.state.TouchAccount(c.Address)

	if callType == runtime.Call {
		// Transfers only allowed on calls
		if err := t.transfer(c.Caller, c.Address, c.Value); err != nil {
			return nil, c.Gas, err
		}
	}

	ret, gas, err := t.run(c, host)
	if err != nil {
		t.state.RevertToSnapshot(snapshot)
	}
	return ret, gas, err
}

var emptyHash types.Hash

func (t *Transition) hasCodeOrNonce(addr types.Address) bool {
	nonce := t.state.GetNonce(addr)
	if nonce != 0 {
		return true
	}
	codeHash := t.state.GetCodeHash(addr)
	if codeHash != emptyCodeHashTwo && codeHash != emptyHash {
		return true
	}
	return false
}

func (t *Transition) applyCreate(msg *runtime.Contract, host runtime.Host) ([]byte, uint64, error) {
	if msg.Depth > int(1024)+1 {
		return nil, msg.Gas, runtime.ErrDepth
	}

	gas := msg.Gas

	// Increment the nonce of the caller
	t.state.IncrNonce(msg.Caller)

	// Check if there if there is a collision and the address already exists
	if t.hasCodeOrNonce(msg.Address) {
		return nil, 0, runtime.ErrContractAddressCollision
	}

	// Take snapshot of the current state
	snapshot := t.state.Snapshot()

	if t.config.EIP158 {
		// Force the creation of the account
		t.state.CreateAccount(msg.Address)
		t.state.IncrNonce(msg.Address)
	}

	// Transfer the value
	if err := t.transfer(msg.Caller, msg.Address, msg.Value); err != nil {
		return nil, gas, runtime.ErrNotEnoughFunds
	}

	code, leftoverGas, err := t.run(msg, host)

	if err != nil {
		t.state.RevertToSnapshot(snapshot)
		return code, leftoverGas, err
	}

	if t.config.EIP158 && len(code) > spuriousDragonMaxCodeSize {
		// Contract size exceeds 'SpuriousDragon' size limit
		t.state.RevertToSnapshot(snapshot)
		return nil, 0, runtime.ErrMaxCodeSizeExceeded
	}

	gasCost := uint64(len(code)) * 200

	if leftoverGas < gasCost {
		// Out of gas creating the contract
		if t.config.Homestead {
			t.state.RevertToSnapshot(snapshot)
			leftoverGas = 0
		}
		return nil, leftoverGas, runtime.ErrCodeStoreOutOfGas
	}

	leftoverGas -= gasCost
	t.state.SetCode(msg.Address, code)

	return code, leftoverGas, nil
}

func (t *Transition) SetStorage(addr types.Address, key types.Hash, value types.Hash, config *chain.ForksInTime) runtime.StorageStatus {
	return t.state.SetStorage(addr, key, value, config)
}

func (t *Transition) GetTxContext() runtime.TxContext {
	return t.ctx
}

func (t *Transition) GetBlockHash(number int64) (res types.Hash) {
	return t.getHash(uint64(number))
}

func (t *Transition) EmitLog(addr types.Address, topics []types.Hash, data []byte) {
	t.state.EmitLog(addr, topics, data)
}

func (t *Transition) GetCodeSize(addr types.Address) int {
	return t.state.GetCodeSize(addr)
}

func (t *Transition) GetCodeHash(addr types.Address) (res types.Hash) {
	return t.state.GetCodeHash(addr)
}

func (t *Transition) GetCode(addr types.Address) []byte {
	return t.state.GetCode(addr)
}

func (t *Transition) GetBalance(addr types.Address) *big.Int {
	return t.state.GetBalance(addr)
}

func (t *Transition) AddBalance(addr types.Address, balance *big.Int) {
	t.state.AddBalance(addr, balance)
}

func (t *Transition) SubBalance(addr types.Address, balance *big.Int) {
	t.state.SubBalance(addr, balance)
}

func (t *Transition) GetStakedBalance(addr types.Address) *big.Int {
	hub := staking.GetStakingHub()
	return hub.GetStakedBalance(addr)
}

func (t *Transition) AddStakedBalance(addr types.Address, balance *big.Int) {
	hub := staking.GetStakingHub()
	hub.IncreaseStake(addr, balance)
}

func (t *Transition) SubStakedBalance(addr types.Address, balance *big.Int) {
	hub := staking.GetStakingHub()
	hub.DecreaseStake(addr, balance)
}

func (t *Transition) GetStorage(addr types.Address, key types.Hash) types.Hash {
	return t.state.GetState(addr, key)
}

func (t *Transition) AccountExists(addr types.Address) bool {
	return t.state.Exist(addr)
}

func (t *Transition) Empty(addr types.Address) bool {
	return t.state.Empty(addr)
}

func (t *Transition) GetNonce(addr types.Address) uint64 {
	return t.state.GetNonce(addr)
}

func (t *Transition) Selfdestruct(addr types.Address, beneficiary types.Address) {
	if !t.state.HasSuicided(addr) {
		t.state.AddRefund(24000)
	}
	t.state.AddBalance(beneficiary, t.state.GetBalance(addr))
	t.state.Suicide(addr)
}

func (t *Transition) Callx(c *runtime.Contract, h runtime.Host) ([]byte, uint64, error) {
	if c.Type == runtime.Create {
		return t.applyCreate(c, h)
	}
	return t.applyCall(c, c.Type, h)
}

func (t *Transition) EmitStakedEvent(staker types.Address, amount *big.Int) {
	t.r.publishStakingEvent(&StakingEvent{
		Type:    StakingEventStaked,
		Number:  t.ctx.Number,
		Address: staker,
		Amount:  amount,
	})
}

func (t *Transition) EmitUnstakedEvent(staker types.Address, amount *big.Int) {
	t.r.publishStakingEvent(&StakingEvent{
		Type:    StakingEventUnstaked,
		Number:  t.ctx.Number,
		Address: staker,
		Amount:  amount,
	})
}
