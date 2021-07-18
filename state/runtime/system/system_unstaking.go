package system

import (
	"errors"
	"math/big"

	"github.com/0xPolygon/minimal/staking"
	"github.com/0xPolygon/minimal/types"
)

// unstakingHandler implements the unstaking logic for the System runtime
type unstakingHandler struct {
	s *System
}

// gas returns the fixed gas price of the unstaking operation
func (uh *unstakingHandler) gas(_ []byte) uint64 {
	return 40000
}

// run executes the system contract unstaking method
// Unstaking is for the FULL amount staked, no partial support is currently present
func (uh *unstakingHandler) run(state *systemState) ([]byte, error) {
	// Grab the staking address
	stakingAddress := types.StringToAddress(StakingAddress)

	// Grab the address calling the staking method
	staker := state.contract.Caller

	// Grab the staked balance for the account
	stakedBalance := state.host.GetStakedBalance(staker)

	// Grab the balance on the staking address
	stakingAccountBalance := state.host.GetBalance(stakingAddress)

	// Sanity check
	// Can't unstake if the balance isn't previously present on the staking account
	// Can't unstake if the value is different from 0
	if stakingAccountBalance.Cmp(stakedBalance) < 0 || state.contract.Value.Cmp(big.NewInt(0)) != 0 {
		return nil, errors.New("Invalid unstake request")
	}

	// Decrease the staked balance on the staking address
	state.host.SubBalance(stakingAddress, stakedBalance)

	// Increase the account's actual balance
	state.host.AddBalance(staker, stakedBalance)

	// Decrease the staked amount from the account's staked balance
	staking.GetStakingHub().AddPendingEvent(staking.PendingEvent{
		Address:   staker,
		Value:     big.NewInt(0),
		EventType: staking.UnstakingEvent,
	})

	state.host.EmitUnstakedEvent(staker, stakedBalance)

	return nil, nil
}
