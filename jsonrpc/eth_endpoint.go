package jsonrpc

import (
	"fmt"
	"math/big"

	"github.com/0xPolygon/minimal/helper/hex"
	"github.com/0xPolygon/minimal/types"
)

// Eth is the eth jsonrpc endpoint
type Eth struct {
	d *Dispatcher
}

// ChainId returns the chain id of the client
func (e *Eth) ChainId() (interface{}, error) {
	return argUintPtr(e.d.chainID), nil
}

// GetBlockByNumber returns information about a block by block number
func (e *Eth) GetBlockByNumber(number BlockNumber, full bool) (interface{}, error) {
	var num uint64
	switch number {
	case LatestBlockNumber:
		num = e.d.store.Header().Number

	case EarliestBlockNumber:
		return nil, fmt.Errorf("fetching the earliest header is not supported")

	case PendingBlockNumber:
		return nil, fmt.Errorf("fetching the pending header is not supported")

	default:
		num = uint64(number)
	}

	block, ok := e.d.store.GetBlockByNumber(num, full)
	if !ok {
		return nil, fmt.Errorf("unable to get block by num %v", num)
	}
	return toBlock(block), nil
}

// GetBlockByHash returns information about a block by hash
func (e *Eth) GetBlockByHash(hash types.Hash, full bool) (interface{}, error) {
	block, ok := e.d.store.GetBlockByHash(hash, full)
	if !ok {
		return nil, fmt.Errorf("unable to get block by hash %v", hash)
	}
	return toBlock(block), nil
}

// BlockNumber returns current block number
func (e *Eth) BlockNumber() (interface{}, error) {
	h := e.d.store.Header()
	if h == nil {
		return nil, fmt.Errorf("header has a nil value")
	}
	return argUintPtr(h.Number), nil
}

// SendRawTransaction sends a raw transaction
func (e *Eth) SendRawTransaction(input string) (interface{}, error) {
	buf := hex.MustDecodeHex(input)

	tx := &types.Transaction{}
	if err := tx.UnmarshalRLP(buf); err != nil {
		return nil, err
	}
	tx.ComputeHash()

	if err := e.d.store.AddTx(tx); err != nil {
		return nil, err
	}
	return tx.Hash.String(), nil
}

// SendTransaction creates new message call transaction or a contract creation, if the data field contains code.
func (e *Eth) SendTransaction(arg *txnArgs) (interface{}, error) {
	transaction, err := e.d.decodeTxn(arg)
	if err != nil {
		return nil, err
	}
	if err := e.d.store.AddTx(transaction); err != nil {
		return nil, err
	}
	return transaction.Hash.String(), nil
}

// GetTransactionByHash returns a transaction by his hash
func (e *Eth) GetTransactionByHash(hash types.Hash) (interface{}, error) {
	blockHash, ok := e.d.store.ReadTxLookup(hash)
	if !ok {
		// txn not found
		return nil, nil
	}
	block, ok := e.d.store.GetBlockByHash(blockHash, true)
	if !ok {
		// block receipts not found
		return nil, nil
	}
	for idx, txn := range block.Transactions {
		if txn.Hash == hash {
			return toTransaction(txn, block, idx), nil
		}
	}
	// txn not found (this should not happen)
	return nil, nil
}

// GetTransactionReceipt returns a transaction receipt by his hash
func (e *Eth) GetTransactionReceipt(hash types.Hash) (interface{}, error) {
	blockHash, ok := e.d.store.ReadTxLookup(hash)
	if !ok {
		// txn not found
		return nil, nil
	}

	block, ok := e.d.store.GetBlockByHash(blockHash, true)
	if !ok {
		fmt.Println("CCCC")
		// block not found
		return nil, nil
	}

	receipts, err := e.d.store.GetReceiptsByHash(blockHash)
	if err != nil {
		fmt.Println("AAAA", err)
		// block receipts not found
		return nil, nil
	}
	if len(receipts) == 0 {
		// receitps not written yet on the db
		fmt.Println("BBBB")
		return nil, nil
	}
	// find the transaction in the body
	indx := -1
	for i, txn := range block.Transactions {
		if txn.Hash == hash {
			indx = i
			break
		}
	}
	if indx == -1 {
		// txn not found
		return nil, nil
	}

	txn := block.Transactions[indx]
	raw := receipts[indx]

	logs := make([]*Log, len(raw.Logs))
	for indx, elem := range raw.Logs {
		logs[indx] = &Log{
			Address:     elem.Address,
			Topics:      elem.Topics,
			Data:        argBytes(elem.Data),
			BlockHash:   block.Hash(),
			BlockNumber: argUint64(block.Number()),
			TxHash:      txn.Hash,
			TxIndex:     argUint64(indx),
			LogIndex:    argUint64(indx),
			Removed:     false,
		}
	}
	res := &receipt{
		Root:              raw.Root,
		CumulativeGasUsed: argUint64(raw.CumulativeGasUsed),
		LogsBloom:         raw.LogsBloom,
		Status:            raw.Status,
		TxHash:            txn.Hash,
		TxIndex:           argUint64(indx),
		BlockHash:         block.Hash(),
		BlockNumber:       argUint64(block.Number()),
		GasUsed:           argUint64(raw.GasUsed),
		ContractAddress:   raw.ContractAddress,
		FromAddr:          txn.From,
		ToAddr:            txn.To,
		Logs:              logs,
	}
	return res, nil
}

// GetStorageAt returns the contract storage at the index position
func (e *Eth) GetStorageAt(address types.Address, index types.Hash, number BlockNumber) (interface{}, error) {
	// Fetch the requested header
	header, err := e.d.getBlockHeaderImpl(number)
	if err != nil {
		return nil, err
	}

	// Get the storage for the passed in location
	result, err := e.d.store.GetStorage(header.StateRoot, address, index)
	if err != nil {
		return nil, err
	}
	return argBytesPtr(result), nil
}

// GasPrice returns the average gas price based on the last x blocks
func (e *Eth) GasPrice() (interface{}, error) {

	// Grab the average gas price and convert it to a hex value
	avgGasPrice := hex.EncodeBig(e.d.store.GetAvgGasPrice())

	return avgGasPrice, nil
}

// Call executes a smart contract call using the transaction object data
func (e *Eth) Call(arg *txnArgs, number BlockNumber) (interface{}, error) {
	transaction, err := e.d.decodeTxn(arg)
	if err != nil {
		return nil, err
	}
	// Fetch the requested header
	header, err := e.d.getBlockHeaderImpl(number)
	if err != nil {
		return nil, err
	}

	// The return value of the execution is saved in the transition (returnValue field)
	returnValue, failed, err := e.d.store.ApplyTxn(header, transaction)
	if err != nil {
		return nil, err
	}

	if failed {
		return nil, fmt.Errorf("unable to execute call")
	}
	return argBytesPtr(returnValue), nil
}

// EstimateGas estimates the gas needed to execute a transaction
func (e *Eth) EstimateGas(arg *txnArgs, rawNum *BlockNumber) (interface{}, error) {
	transaction, err := e.d.decodeTxn(arg)
	if err != nil {
		return nil, err
	}

	const standardGas uint64 = 21000

	number := LatestBlockNumber
	if rawNum != nil {
		number = *rawNum
	}

	// Fetch the requested header
	header, err := e.d.getBlockHeaderImpl(number)
	if err != nil {
		return nil, err
	}

	var (
		lowEnd  = standardGas
		highEnd uint64
		gasCap  uint64
	)

	// If the gas limit was passed in, use it as a ceiling
	if transaction.Gas != 0 && uint64(transaction.Gas) >= standardGas {
		highEnd = uint64(transaction.Gas)
	} else {
		// If not, use the referenced block number
		highEnd = header.GasLimit
	}

	gasPriceInt := new(big.Int).Set(transaction.GasPrice)
	valueInt := new(big.Int).Set(transaction.Value)

	// If the sender address is present, recalculate the ceiling to his balance
	if transaction.GasPrice != nil && gasPriceInt.BitLen() != 0 {

		// Get the account balance
		acc, err := e.d.store.GetAccount(header.StateRoot, transaction.From)
		if err != nil {
			return nil, err
		}

		available := new(big.Int).Set(acc.Balance)

		if transaction.Value != nil {
			if valueInt.Cmp(available) >= 0 {
				return 0, fmt.Errorf("insufficient funds for transfer")
			}

			available.Sub(available, valueInt)
		}

		allowance := new(big.Int).Div(available, gasPriceInt)

		// If the allowance is larger than maximum uint64, skip checking
		if allowance.IsUint64() && highEnd > allowance.Uint64() {
			highEnd = allowance.Uint64()
		}
	}

	if highEnd > types.GasCap.Uint64() {
		// The high end is greater than the environment gas cap
		highEnd = types.GasCap.Uint64()
	}

	gasCap = highEnd

	// Run the transaction with the estimated gas
	testTransaction := func(gas uint64) (bool, error) {
		// Create a dummy transaction with the new gas
		txn := transaction.Copy()
		txn.Gas = gas

		_, failed, err := e.d.store.ApplyTxn(header, txn)
		if err != nil {
			return failed, err
		}

		return failed, nil
	}

	// Start the binary search for the lowest possible gas price
	for lowEnd <= highEnd {
		mid := (lowEnd + highEnd) / 2

		failed, err := testTransaction(mid)
		if err != nil {
			return 0, err
		}

		if failed {
			// If the transaction failed => increase the gas
			lowEnd = mid + 1
		} else {
			// If the transaction didn't fail => lower the gas
			highEnd = mid - 1
		}
	}

	// we stopped the binary search at the last gas limit
	// at which the txn could not be executed
	highEnd += 1

	// Check the edge case if even the highest cap is not enough to complete the transaction
	if highEnd == gasCap {
		failed, err := testTransaction(gasCap)

		if err != nil {
			return 0, err
		}

		if failed {
			return 0, fmt.Errorf("gas required exceeds allowance (%d)", gasCap)
		}
	}

	return hex.EncodeUint64(highEnd), nil
}

// GetLogs returns an array of logs matching the filter options
func (e *Eth) GetLogs(filterOptions *LogFilter) (interface{}, error) {
	var result []*Log
	parseReceipts := func(header *types.Header) error {
		receipts, err := e.d.store.GetReceiptsByHash(header.Hash)
		if err != nil {
			return err
		}

		for indx, receipt := range receipts {
			for logIndx, log := range receipt.Logs {
				if filterOptions.Match(log) {
					result = append(result, &Log{
						Address:     log.Address,
						Topics:      log.Topics,
						Data:        argBytes(log.Data),
						BlockNumber: argUint64(header.Number),
						BlockHash:   header.Hash,
						TxHash:      receipt.TxHash,
						TxIndex:     argUint64(indx),
						LogIndex:    argUint64(logIndx),
					})
				}
			}
		}
		return nil
	}

	if filterOptions.BlockHash != nil {
		block, ok := e.d.store.GetBlockByHash(*filterOptions.BlockHash, false)
		if !ok {
			return nil, fmt.Errorf("not found")
		}
		if err := parseReceipts(block.Header); err != nil {
			return nil, err
		}
		return result, nil
	}

	head := e.d.store.Header().Number

	resolveNum := func(num BlockNumber) uint64 {
		if num == PendingBlockNumber || num == EarliestBlockNumber {
			num = LatestBlockNumber
		}
		if num == LatestBlockNumber {
			return head
		}
		return uint64(num)
	}

	from := resolveNum(filterOptions.fromBlock)
	to := resolveNum(filterOptions.toBlock)

	if to < from {
		return nil, fmt.Errorf("incorrect range")
	}
	for i := from; i < to; i++ {
		header, ok := e.d.store.GetHeaderByNumber(i)
		if !ok {
			break
		}
		if header.Number == 0 {
			// do not check logs in genesis
			continue
		}
		if err := parseReceipts(header); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// GetBalance returns the account's balance at the referenced block
func (e *Eth) GetBalance(address types.Address, number BlockNumber) (interface{}, error) {
	header, err := e.d.getBlockHeaderImpl(number)
	if err != nil {
		return nil, err
	}

	acc, err := e.d.store.GetAccount(header.StateRoot, address)
	if err != nil {
		// Account not found, return an empty account
		return argUintPtr(0), nil
	}

	return argBigPtr(acc.Balance), nil
}

// GetTransactionCount returns account nonce
func (e *Eth) GetTransactionCount(address types.Address, number BlockNumber) (interface{}, error) {
	nonce, err := e.d.getNextNonce(address, number)
	if err != nil {
		return nil, err
	}
	return argUintPtr(nonce), nil
}

// GetCode returns account code at given block number
func (e *Eth) GetCode(address types.Address, number BlockNumber) (interface{}, error) {
	header, err := e.d.getBlockHeaderImpl(number)
	if err != nil {
		return nil, err
	}

	acc, err := e.d.store.GetAccount(header.StateRoot, address)
	if err != nil {
		return "0x", nil
	}

	emptySlice := []byte{}
	code, err := e.d.store.GetCode(types.BytesToHash(acc.CodeHash))
	if err != nil {
		// TODO This is just a workaround. Figure out why CodeHash is populated for regular accounts
		return argBytesPtr(emptySlice), nil
	}

	return argBytesPtr(code), nil
}

// NewFilter creates a filter object, based on filter options, to notify when the state changes (logs).
func (e *Eth) NewFilter(filter *LogFilter) (interface{}, error) {
	return e.d.filterManager.NewLogFilter(filter, nil), nil
}

// NewBlockFilter creates a filter in the node, to notify when a new block arrives
func (e *Eth) NewBlockFilter() (interface{}, error) {
	return e.d.filterManager.NewBlockFilter(nil), nil
}

// GetFilterChanges is a polling method for a filter, which returns an array of logs which occurred since last poll.
func (e *Eth) GetFilterChanges(id string) (interface{}, error) {
	return e.d.filterManager.GetFilterChanges(id)
}

// UninstallFilter uninstalls a filter with given ID
func (e *Eth) UninstallFilter(id string) (bool, error) {
	ok := e.d.filterManager.Uninstall(id)
	return ok, nil
}

// Unsubscribe uninstalls a filter in a websocket
func (e *Eth) Unsubscribe(id string) (bool, error) {
	ok := e.d.filterManager.Uninstall(id)
	return ok, nil
}
