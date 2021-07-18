package staking

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/0xPolygon/minimal/types"
	"github.com/hashicorp/go-hclog"
)

type StakingEventType string

var (
	StakingEvent   StakingEventType = "staking"
	UnstakingEvent StakingEventType = "unstaking"
)

var (
	defaultFileName = "staking-map.json"
)

// StakingHub acts as a hub (manager) for staked account balances
type StakingHub struct {
	// Address -> Stake
	StakingMap map[types.Address]*big.Int

	// Specifies the working directory for the Polygon SDK (location for writeback)
	WorkingDirectory string

	// Write-back period (in s) for backup staking data
	WritebackPeriod time.Duration

	// Logger for logging errors and information
	Logger hclog.Logger

	// Event queue defines staking / unstaking events
	// which are read by modules that do final transaction sealing
	EventQueue []PendingEvent

	// Event list mutex
	EventQueueMutex sync.Mutex

	// Staking map mutex
	StakingMutex sync.Mutex

	// Close channel
	CloseCh chan struct {
	}
}

var stakingHubInstance StakingHub
var once sync.Once

// GetStakingHub initializes the stakingHubInstance singleton
func GetStakingHub() *StakingHub {
	once.Do(func() {
		stakingHubInstance = StakingHub{
			StakingMap: make(map[types.Address]*big.Int),
			EventQueue: make([]PendingEvent, 0),
			CloseCh:    make(chan struct{}),
		}
	})

	return &stakingHubInstance
}

// SetWorkingDirectory sets the writeback directory for the staking map
func (sh *StakingHub) SetWorkingDirectory(directory string) {
	sh.WorkingDirectory = directory

	err := sh.readFromDisk()
	if err != nil {
		// Log as warning because reading this map is not crucial
		sh.log(err.Error(), logWarning)
	}

	go sh.saveToDisk()
}

// LogType defines possible log types for the logger
type LogType string

var (
	logInfo    LogType = "info"
	logError   LogType = "error"
	logWarning LogType = "warning"
)

// log logs the output to the console if the logger is set
func (sh *StakingHub) log(output string, logType LogType) {
	if sh.Logger != nil {
		switch logType {
		case logInfo:
			sh.Logger.Info(output)
		case logError:
			sh.Logger.Error(output)
		case logWarning:
			sh.Logger.Warn(output)
		}
	}
}

// SetLogger sets the StakingHub logger
func (sh *StakingHub) SetLogger(logger hclog.Logger) {
	sh.Logger = logger.Named("staking-hub")
}

// CloseStakingHub stops the writeback process
func (sh *StakingHub) CloseStakingHub() {
	sh.StakingMutex.Lock()
	defer sh.StakingMutex.Unlock()

	// Alert the closing channel
	sh.CloseCh <- struct{}{}

	close(sh.CloseCh)
}

// PendingEvent contains useful information about a staking / unstaking event
type PendingEvent struct {
	Address   types.Address
	Value     *big.Int
	EventType StakingEventType
}

// Compare checks if the two events match
func (pe *PendingEvent) Compare(event PendingEvent) bool {
	if pe.EventType == event.EventType &&
		pe.Address.String() == event.Address.String() &&
		pe.Value.Cmp(event.Value) == 0 {
		return true
	}

	return false
}

// AddPendingEvent pushes an event to the event queue
func (sh *StakingHub) AddPendingEvent(event PendingEvent) {
	sh.EventQueueMutex.Lock()
	defer sh.EventQueueMutex.Unlock()

	// Check to avoid double addition
	if !sh.hasEvent(event) {
		sh.EventQueue = append(sh.EventQueue, event)
	}
}

// RemovePendingEvent removes the pending event from the event queue if it exists
func (sh *StakingHub) RemovePendingEvent(event PendingEvent) {
	sh.EventQueueMutex.Lock()
	defer sh.EventQueueMutex.Unlock()

	foundIndx := -1
	for indx, el := range sh.EventQueue {
		if el.Compare(event) {
			foundIndx = indx
		}
	}

	if foundIndx >= 0 {
		sh.EventQueue = append(sh.EventQueue[:foundIndx], sh.EventQueue[foundIndx+1:]...)
	}
}

// hasEvent checks if an identical event is present in the queue. Not thread safe
func (sh *StakingHub) hasEvent(event PendingEvent) bool {
	for _, el := range sh.EventQueue {
		if el.Compare(event) {
			return true
		}
	}

	return false
}

// ContainsPendingEvent checks if an identical event is present in the queue
func (sh *StakingHub) ContainsPendingEvent(event PendingEvent) bool {
	sh.EventQueueMutex.Lock()
	defer sh.EventQueueMutex.Unlock()

	return sh.hasEvent(event)
}

// saveToDisk is a helper method for periodically saving the stake data to disk
func (sh *StakingHub) saveToDisk() {
	for {
		select {
		case <-sh.CloseCh:
			return
		default:
		}

		// Save the current staking map to disk, in JSON
		mappings := sh.GetStakerMappings()

		reader, err := sh.marshalJSON(mappings)
		if err != nil {
			continue
		}

		// Save the json to workingDirectory/StakingMap.json
		file, err := os.Create(filepath.Join(sh.WorkingDirectory, defaultFileName))
		if err != nil {
			_ = file.Close()
			sh.log("unable to create writeback file", logError)

			continue
		}

		_, err = io.Copy(file, reader)
		if err != nil {
			sh.log("unable to write date into writeback file", logError)
		}

		err = file.Close()
		if err != nil {
			sh.log("unable to close writeback file", logError)
		}

		// Sleep for the writeback period
		time.Sleep(sh.WritebackPeriod * time.Second)
	}
}

// readFromDisk reads the staking map from a previously saved disk copy, if it exists
func (sh *StakingHub) readFromDisk() error {
	// Check if the file exists
	mapFile, err := os.Open(filepath.Join(sh.WorkingDirectory, defaultFileName))
	if err != nil {
		return fmt.Errorf("no exiting map file is present")
	}

	byteValue, _ := ioutil.ReadAll(mapFile)

	var stakingMap []StakerMapping

	// Unmarshal the json
	if err = json.Unmarshal(byteValue, &stakingMap); err != nil {
		return fmt.Errorf("unable to unmarshal staking map")
	}

	// Close the file
	if err = mapFile.Close(); err != nil {
		return fmt.Errorf("unable to close staking file")
	}

	// Update the staking map using the read data
	sh.StakingMutex.Lock()
	defer sh.StakingMutex.Unlock()

	for _, val := range stakingMap {
		sh.StakingMap[val.Address] = val.Stake
	}

	return nil
}

// marshalJSON generates the json object for staker mappings
func (sh *StakingHub) marshalJSON(mappings []StakerMapping) (io.Reader, error) {
	sh.StakingMutex.Lock()
	defer sh.StakingMutex.Unlock()

	b, err := json.MarshalIndent(mappings, "", "\t")
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

// isStaker is a helper method to check whether or not an address has a staked balance
func (sh *StakingHub) isStaker(address types.Address) bool {
	if _, ok := sh.StakingMap[address]; ok {
		return true
	}

	return false
}

// IncreaseStake increases the account's staked balance, or sets it if the account wasn't
// in the StakingMap
func (sh *StakingHub) IncreaseStake(address types.Address, stakeBalance *big.Int) {
	sh.StakingMutex.Lock()
	defer sh.StakingMutex.Unlock()

	if !sh.isStaker(address) {
		sh.StakingMap[address] = stakeBalance
	} else {
		sh.StakingMap[address] = big.NewInt(0).Add(sh.StakingMap[address], stakeBalance)
	}

	sh.log(
		fmt.Sprintf("Stake increase:\t%s %s", address.String(), stakeBalance.String()),
		logInfo,
	)
}

// DecreaseStake decreases the account's staked balance if the account is present
func (sh *StakingHub) DecreaseStake(address types.Address, unstakeBalance *big.Int) {
	sh.StakingMutex.Lock()
	defer sh.StakingMutex.Unlock()

	if sh.isStaker(address) && sh.StakingMap[address].Cmp(unstakeBalance) >= 0 {
		sh.StakingMap[address] = big.NewInt(0).Sub(sh.StakingMap[address], unstakeBalance)

		sh.log(
			fmt.Sprintf("Stake decrease:\t%s %s", address.String(), unstakeBalance.String()),
			logInfo,
		)
	}
}

// ResetStake resets the account's staked balance
func (sh *StakingHub) ResetStake(address types.Address) {
	sh.StakingMutex.Lock()
	defer sh.StakingMutex.Unlock()

	if sh.isStaker(address) {
		delete(sh.StakingMap, address)
	}

	sh.log(
		fmt.Sprintf("Stake reset:\t%s", address.String()),
		logInfo,
	)
}

// GetStakedBalance returns an accounts staked balance if it is a staker.
// Returns 0 if the address is not a staker
func (sh *StakingHub) GetStakedBalance(address types.Address) *big.Int {
	sh.StakingMutex.Lock()
	defer sh.StakingMutex.Unlock()

	if sh.isStaker(address) {
		return sh.StakingMap[address]
	}

	return big.NewInt(0)
}

// GetStakerAddresses returns a list of all addresses that have a stake > 0
func (sh *StakingHub) GetStakerAddresses() []types.Address {
	sh.StakingMutex.Lock()
	defer sh.StakingMutex.Unlock()

	stakers := make([]types.Address, len(sh.StakingMap))

	var indx = 0
	for address := range sh.StakingMap {
		stakers[indx] = address
		indx++
	}

	return stakers
}

// StakerMapping is a representation of a staked account balance
type StakerMapping struct {
	Address types.Address `json:"address"`
	Stake   *big.Int      `json:"stake"`
}

// GetStakerMappings returns the staking addresses and their staking balances
func (sh *StakingHub) GetStakerMappings() []StakerMapping {
	sh.StakingMutex.Lock()
	defer sh.StakingMutex.Unlock()

	mappings := make([]StakerMapping, len(sh.StakingMap))

	var indx = 0
	for address, stake := range sh.StakingMap {
		mappings[indx] = StakerMapping{
			Address: address,
			Stake:   stake,
		}
		indx++
	}

	return mappings
}
