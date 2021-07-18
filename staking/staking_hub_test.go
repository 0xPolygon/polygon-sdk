package staking

import (
	"math/big"
	"testing"

	"github.com/0xPolygon/minimal/types"
	"github.com/stretchr/testify/assert"
)

func EthToWei(ethValue int64) *big.Int {
	return new(big.Int).Mul(
		big.NewInt(ethValue),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

type testStakeMapping struct {
	address types.Address
	stake   *big.Int
}

func instantiateStakingMap(mappings []testStakeMapping) {
	hub := GetStakingHub()

	hub.StakingMutex.Lock()
	for _, stakePair := range mappings {
		hub.StakingMap[stakePair.address] = stakePair.stake
	}
	hub.StakingMutex.Unlock()
}

func TestStakingHub_GetInstance(t *testing.T) {
	testTable := []struct {
		name         string
		numInstances int
	}{
		{
			"Valid instance",
			1,
		},
		{
			"Multiple instance calls",
			5,
		},
	}

	for indx, testCase := range testTable {
		t.Run(testCase.name, func(t *testing.T) {
			instances := make([]*StakingHub, testCase.numInstances)

			for i := 0; i < testCase.numInstances; i++ {
				instances[i] = GetStakingHub()
			}

			for i := 0; i < testCase.numInstances; i++ {
				assert.NotNilf(t, instances[i], "Staking hub instance invalid")
			}

			if indx == 1 && testCase.numInstances > 1 {
				// Check if the instances reference the same singleton
				dummyDir := "dummyDir"
				instances[0].SetWorkingDirectory(dummyDir)

				for i := 1; i < testCase.numInstances; i++ {
					assert.Equalf(t,
						instances[0].WorkingDirectory,
						instances[i].WorkingDirectory,
						"Staking hub instances not equal",
					)
				}
			}
		})
	}
}

func TestStakingHub_GetStakedBalance(t *testing.T) {
	validStakes := []struct {
		address      types.Address
		initialStake *big.Int
	}{
		// Valid account #1
		{
			types.StringToAddress("123"),
			EthToWei(50), // 50 ETH
		},

		// Valid account #2
		{
			types.StringToAddress("456"),
			EthToWei(10),
		},

		// Valid account #3
		{
			types.StringToAddress("789"),
			EthToWei(5),
		},
	}

	hub := GetStakingHub()

	// Instantiate the staking map
	for _, stakePair := range validStakes {
		hub.StakingMap[stakePair.address] = stakePair.initialStake
	}

	// Check the balances
	for _, stakePair := range validStakes {
		balance := hub.GetStakedBalance(stakePair.address)

		assert.Equalf(t, stakePair.initialStake, balance, "Staked balance doesn't match")
	}
}

func TestStakingHub_IncreaseStake(t *testing.T) {
	validStakes := []struct {
		address        types.Address
		initialStake   *big.Int
		increaseAmount *big.Int
	}{
		// Valid account #1
		{
			types.StringToAddress("123"),
			EthToWei(50), // 50 ETH
			EthToWei(5),  // 50 ETH
		},

		// Valid account #2
		{
			types.StringToAddress("456"),
			EthToWei(10),
			EthToWei(1),
		},

		// Valid account #3
		{
			types.StringToAddress("789"),
			EthToWei(0),
			EthToWei(10),
		},
	}

	hub := GetStakingHub()
	// Instantiate the staking map
	var mappings []testStakeMapping
	for _, stakePair := range validStakes {
		mappings = append(mappings, testStakeMapping{
			address: stakePair.address,
			stake:   stakePair.initialStake,
		})
	}

	instantiateStakingMap(mappings)

	// Increase the stake
	for _, stakePair := range validStakes {
		expectedStake := big.NewInt(0).Add(stakePair.initialStake, stakePair.increaseAmount)

		hub.IncreaseStake(stakePair.address, stakePair.increaseAmount)

		assert.Equalf(t, expectedStake, hub.GetStakedBalance(stakePair.address), "Stake amount not valid")
	}
}

func TestStakingHub_DecreaseStake(t *testing.T) {
	validStakes := []struct {
		address        types.Address
		initialStake   *big.Int
		decreaseAmount *big.Int
	}{
		// Valid account #1
		{
			types.StringToAddress("123"),
			EthToWei(50), // 50 ETH
			EthToWei(5),  // 50 ETH
		},

		// Valid account #2
		{
			types.StringToAddress("456"),
			EthToWei(10),
			EthToWei(1),
		},

		// Invalid account #3
		{
			types.StringToAddress("789"),
			EthToWei(0),
			EthToWei(10),
		},
	}

	hub := GetStakingHub()

	// Instantiate the staking map
	var mappings []testStakeMapping
	for _, stakePair := range validStakes {
		mappings = append(mappings, testStakeMapping{
			address: stakePair.address,
			stake:   stakePair.initialStake,
		})
	}
	instantiateStakingMap(mappings)

	// Decrease the stake
	for _, stakePair := range validStakes {
		var expectedStake *big.Int

		if stakePair.initialStake.Cmp(stakePair.decreaseAmount) < 0 {
			// Decrease should have no effect
			expectedStake = stakePair.initialStake
		} else {
			expectedStake = big.NewInt(0).Sub(stakePair.initialStake, stakePair.decreaseAmount)
		}

		hub.DecreaseStake(stakePair.address, stakePair.decreaseAmount)

		assert.Equalf(t, expectedStake, hub.GetStakedBalance(stakePair.address), "Stake amount not valid")
	}
}

func TestStakingHub_ResetStake(t *testing.T) {
	validStakes := []struct {
		address      types.Address
		initialStake *big.Int
	}{
		// Valid account #1
		{
			types.StringToAddress("123"),
			EthToWei(50), // 50 ETH
		},

		// Valid account #2
		{
			types.StringToAddress("456"),
			EthToWei(10),
		},

		// Valid account #3
		{
			types.StringToAddress("789"),
			EthToWei(0),
		},
	}

	hub := GetStakingHub()

	// Instantiate the staking map
	var mappings []testStakeMapping
	for _, stakePair := range validStakes {
		mappings = append(mappings, testStakeMapping{
			address: stakePair.address,
			stake:   stakePair.initialStake,
		})
	}
	instantiateStakingMap(mappings)

	// Reset the stake
	for _, stakePair := range validStakes {
		hub.ResetStake(stakePair.address)

		assert.Equalf(t, big.NewInt(0), hub.GetStakedBalance(stakePair.address), "Stake amount not reset")
	}
}
