package jsonrpc

import (
	"github.com/0xPolygon/minimal/staking"
	"github.com/0xPolygon/minimal/types"
)

// Stake is the main implementation hub
// for IBFT PoS JSON-RPC endpoints
type Stake struct {
	d *Dispatcher
}

// GetStakedBalance returns the account's staked balance at the referenced block
func (s *Stake) GetStakedBalance(address types.Address, number BlockNumber) (interface{}, error) {
	header, err := s.d.getBlockHeaderImpl(number)
	if err != nil {
		return nil, err
	}

	_, err = s.d.store.GetAccount(header.StateRoot, address)
	if err != nil {
		// Account not found, return an empty account
		return argUintPtr(0), nil
	}

	stakingHub := staking.GetStakingHub()

	return argBigPtr(stakingHub.GetStakedBalance(address)), nil
}
