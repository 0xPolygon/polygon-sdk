package e2e

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/minimal/e2e/framework"
	"github.com/0xPolygon/minimal/state/runtime/system"
	"github.com/0xPolygon/minimal/types"
	"github.com/stretchr/testify/assert"
	"github.com/umbracle/go-web3"
	"github.com/umbracle/go-web3/jsonrpc"
)

type addressKeyPair struct {
	privateKey *ecdsa.PrivateKey
	address    types.Address
}

// generateAddressKeyPairs is a helper method for generating signing keys and addresses
func generateAddressKeyPairs(num int, t *testing.T) []*addressKeyPair {
	var pairs []*addressKeyPair

	for i := 0; i < num; i++ {
		senderKey, senderAddr := framework.GenerateKeyAndAddr(t)
		pairs = append(pairs, &addressKeyPair{address: senderAddr, privateKey: senderKey})
	}

	return pairs
}

// getAccountBalance is a helper method for fetching the Balance field of an account
func getAccountBalance(
	address types.Address,
	rpcClient *jsonrpc.Client,
	t *testing.T,
) *big.Int {
	accountBalance, err := rpcClient.Eth().GetBalance(
		web3.Address(address),
		web3.Latest,
	)

	assert.NoError(t, err)

	return accountBalance
}

// getStakedBalance is a helper method for fetching the StakedBalance field of an account
func getStakedBalance(
	address types.Address,
	rpcClient *jsonrpc.Client,
	t *testing.T,
) *big.Int {
	var out string
	if callErr := rpcClient.Call(
		"stake_getStakedBalance",
		&out,
		web3.Address(address),
		web3.Latest.String(),
	); callErr != nil {
		t.Fatalf("Unable to fetch staked balance")
	}

	stakedBalance, ok := new(big.Int).SetString(out[2:], 16)
	if !ok {
		t.Fatalf("Unable to convert staked balance response to big.Int")
	}

	return stakedBalance
}

var defaultGasPrice = big.NewInt(1048576)

func TestSystem_StakeAmount(t *testing.T) {
	addressKeyPairs := generateAddressKeyPairs(2, t)

	validAccounts := []struct {
		address types.Address
		balance *big.Int
	}{
		// Valid account #1
		{
			addressKeyPairs[0].address,
			framework.EthToWei(50), // 50 ETH
		},
		// Empty account
		{
			addressKeyPairs[1].address,
			big.NewInt(0),
		},
	}

	stakingAddress := types.StringToAddress(system.StakingAddress)

	testTable := []struct {
		name          string
		staker        types.Address
		stakeAmount   *big.Int
		shouldSucceed bool
	}{
		{
			"Valid stake",
			validAccounts[0].address,
			framework.EthToWei(10),
			true,
		},
		{
			"Invalid stake",
			validAccounts[1].address,
			framework.EthToWei(100),
			false,
		},
	}

	srvs := framework.NewTestServers(t, 1, func(config *framework.TestServerConfig) {
		config.SetConsensus(framework.ConsensusDev)
		config.SetSeal(true)

		for _, acc := range validAccounts {
			config.Premine(acc.address, acc.balance)
		}
	})
	srv := srvs[0]

	rpcClient := srv.JSONRPC()
	for indx, testCase := range testTable {
		t.Run(testCase.name, func(t *testing.T) {
			// Fetch the staker balance before sending the transaction
			accountBalance := getAccountBalance(testCase.staker, rpcClient, t)
			stakedBalance := getStakedBalance(testCase.staker, rpcClient, t)

			// Set the preSend balances
			previousAccountBalance, _ := big.NewInt(0).SetString(accountBalance.String(), 10)
			previousStakedBalance, _ := big.NewInt(0).SetString(stakedBalance.String(), 10)

			preparedTxn := &framework.PreparedTransaction{
				From:     testCase.staker,
				To:       &stakingAddress,
				GasPrice: defaultGasPrice,
				Gas:      1000000,
				Value:    testCase.stakeAmount,
			}

			fee := big.NewInt(0)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			receipt, err := srv.SendRawTx(ctx, preparedTxn, addressKeyPairs[indx].privateKey)

			assert.NoErrorf(t, err, "Unable to send raw tx")
			assert.NotNilf(t, receipt, "Receipt is nil")

			if testCase.shouldSucceed {
				fee = new(big.Int).Mul(
					big.NewInt(int64(receipt.GasUsed)),
					big.NewInt(defaultGasPrice.Int64()),
				)
			}

			// Fetch the balance after sending
			accountBalance = getAccountBalance(testCase.staker, rpcClient, t)
			stakedBalance = getStakedBalance(testCase.staker, rpcClient, t)

			accountBalanceExpected := previousAccountBalance
			if testCase.shouldSucceed {
				accountBalanceExpected = previousAccountBalance.Sub(
					previousAccountBalance,
					new(big.Int).Add(testCase.stakeAmount, fee),
				)
			}

			stakedBalanceExpected := previousStakedBalance
			if testCase.shouldSucceed {
				stakedBalanceExpected = previousStakedBalance.Add(
					previousStakedBalance,
					testCase.stakeAmount,
				)
			}

			// Check the balances
			assert.Equalf(t,
				accountBalanceExpected,
				accountBalance,
				"Account balance incorrect")

			assert.Equalf(t,
				stakedBalanceExpected,
				stakedBalance,
				"Staked balance incorrect")
		})
	}
}

func TestSystem_UnstakeAmount(t *testing.T) {
	addressKeyPairs := generateAddressKeyPairs(2, t)

	stakingAddress := types.StringToAddress(system.StakingAddress)
	unstakingAddress := types.StringToAddress(system.UnstakingAddress)

	validAccounts := []struct {
		address       types.Address
		balance       *big.Int
		stakedBalance *big.Int
	}{
		// Staking address initialization
		{
			stakingAddress,
			framework.EthToWei(10), // 10 ETH has been staked in the past
			framework.EthToWei(0),
		},
		// Valid account with stake
		{
			addressKeyPairs[0].address,
			framework.EthToWei(50), // 50 ETH
			framework.EthToWei(10), // 10 ETH
		},
		// Valid account without stake
		{
			addressKeyPairs[1].address,
			framework.EthToWei(0),
			framework.EthToWei(0),
		},
	}

	testTable := []struct {
		name          string
		staker        types.Address
		unstakeAmount *big.Int
		shouldSucceed bool
	}{
		{
			"Valid unstake",
			validAccounts[1].address,
			framework.EthToWei(10),
			true,
		},
		{
			"Invalid unstake",
			validAccounts[2].address,
			framework.EthToWei(100),
			false,
		},
	}

	srvs := framework.NewTestServers(t, 1, func(config *framework.TestServerConfig) {
		config.SetConsensus(framework.ConsensusDev)
		config.SetSeal(true)

		for _, acc := range validAccounts {
			config.PremineWithStake(acc.address, acc.balance, acc.stakedBalance)
		}
	})
	srv := srvs[0]

	rpcClient := srv.JSONRPC()
	for indx, testCase := range testTable {
		t.Run(testCase.name, func(t *testing.T) {
			// Fetch the staker balance before sending the transaction
			accountBalance := getAccountBalance(testCase.staker, rpcClient, t)
			stakedBalance := getStakedBalance(testCase.staker, rpcClient, t)

			// Set the preSend balances
			previousAccountBalance, _ := big.NewInt(0).SetString(accountBalance.String(), 10)
			previousStakedBalance, _ := big.NewInt(0).SetString(stakedBalance.String(), 10)

			// Do the transfer
			preparedTxn := &framework.PreparedTransaction{
				From:     testCase.staker,
				To:       &unstakingAddress,
				GasPrice: defaultGasPrice,
				Gas:      1000000,
				Value:    big.NewInt(0),
			}

			fee := big.NewInt(0)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			receipt, err := srv.SendRawTx(ctx, preparedTxn, addressKeyPairs[indx].privateKey)
			assert.NoError(t, err)
			assert.NotNil(t, receipt)

			if testCase.shouldSucceed {
				fee = new(big.Int).Mul(
					big.NewInt(int64(receipt.GasUsed)),
					big.NewInt(defaultGasPrice.Int64()),
				)
			}

			// Fetch the balance after sending
			accountBalance = getAccountBalance(testCase.staker, rpcClient, t)
			stakedBalance = getStakedBalance(testCase.staker, rpcClient, t)

			accountBalanceExpected := previousAccountBalance
			if testCase.shouldSucceed {
				accountBalanceExpected = previousAccountBalance.Add(
					previousAccountBalance,
					new(big.Int).Sub(testCase.unstakeAmount, fee),
				)
			}

			stakedBalanceExpected := previousStakedBalance
			if testCase.shouldSucceed {
				stakedBalanceExpected = previousStakedBalance.Sub(
					previousStakedBalance,
					testCase.unstakeAmount,
				)
			}

			// Check the balances
			assert.Equalf(t,
				accountBalanceExpected.Cmp(accountBalance),
				0,
				"Account balance incorrect")

			assert.Equalf(t,
				stakedBalanceExpected.Cmp(stakedBalance),
				0,
				"Staked balance incorrect")
		})
	}
}
