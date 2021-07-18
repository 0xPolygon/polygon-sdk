package e2e

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/minimal/consensus/ibft"
	"github.com/umbracle/go-web3"

	"github.com/0xPolygon/minimal/e2e/framework"
	"github.com/0xPolygon/minimal/types"
	"github.com/stretchr/testify/assert"
)

func TestIbft_Transfer(t *testing.T) {
	senderKey, senderAddr := framework.GenerateKeyAndAddr(t)
	_, receiverAddr := framework.GenerateKeyAndAddr(t)

	ibftManager := framework.NewIBFTServersManager(t, IBFTMinNodes, IBFTDirPrefix, func(i int, config *framework.TestServerConfig) {
		config.Premine(senderAddr, framework.EthToWei(10))
		config.PremineValidatorBalance(big.NewInt(0), framework.EthToWei(10))
		config.SetSeal(true)
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ibftManager.StartServers(ctx)

	srv := ibftManager.GetServer(0)
	for i := 0; i < IBFTMinNodes-1; i++ {
		txn := &framework.PreparedTransaction{
			From:     senderAddr,
			To:       &receiverAddr,
			GasPrice: big.NewInt(10000),
			Gas:      1000000,
			Value:    framework.EthToWei(1),
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		receipt, err := srv.SendRawTx(ctx, txn, senderKey)

		assert.NoError(t, err)
		assert.NotNilf(t, receipt, "Receipt is nil")
		assert.NotNil(t, receipt.TransactionHash)
	}
}

func TestIbft_TransactionFeeRecipient(t *testing.T) {
	testCases := []struct {
		name         string
		contractCall bool
		txAmount     *big.Int
	}{
		{
			name:         "transfer transaction",
			contractCall: false,
			txAmount:     framework.EthToWei(1),
		},
		{
			name:         "contract function execution",
			contractCall: true,
			txAmount:     big.NewInt(0),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			senderKey, senderAddr := framework.GenerateKeyAndAddr(t)
			_, receiverAddr := framework.GenerateKeyAndAddr(t)

			ibftManager := framework.NewIBFTServersManager(t, IBFTMinNodes, IBFTDirPrefix, func(i int, config *framework.TestServerConfig) {
				config.Premine(senderAddr, framework.EthToWei(10))
				config.PremineValidatorBalance(big.NewInt(0), framework.EthToWei(10))
				config.SetSeal(true)
			})

			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			ibftManager.StartServers(ctx)

			srv := ibftManager.GetServer(0)
			clt := srv.JSONRPC()

			txn := &framework.PreparedTransaction{
				From:     senderAddr,
				To:       &receiverAddr,
				GasPrice: big.NewInt(10000),
				Gas:      1000000,
				Value:    tc.txAmount,
			}

			if tc.contractCall {
				// Deploy contract
				deployTx := &framework.PreparedTransaction{
					From:     senderAddr,
					GasPrice: big.NewInt(0),
					Gas:      1000000,
					Value:    big.NewInt(0),
					Input:    framework.MethodSig("setA1"),
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				receipt, err := srv.SendRawTx(ctx, deployTx, senderKey)
				assert.NoError(t, err)
				assert.NotNil(t, receipt)

				contractAddr := types.Address(receipt.ContractAddress)
				txn.To = &contractAddr
				txn.Input = framework.MethodSig("setA1")
			}

			ctx1, cancel1 := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel1()
			receipt, err := srv.SendRawTx(ctx1, txn, senderKey)
			assert.NoError(t, err)
			assert.NotNil(t, receipt)

			// Get the block proposer from the extra data seal
			assert.NotNil(t, receipt.BlockHash)
			block, err := clt.Eth().GetBlockByHash(receipt.BlockHash, false)
			assert.NoError(t, err)
			extraData := &ibft.IstanbulExtra{}
			extraDataWithoutVanity := block.ExtraData[ibft.IstanbulExtraVanity:]
			err = extraData.UnmarshalRLP(extraDataWithoutVanity)
			assert.NoError(t, err)

			proposerAddr, err := framework.EcrecoverFromBlockhash(types.Hash(block.Hash), extraData.Seal)
			assert.NoError(t, err)

			// Given that this is the first transaction on the blockchain, proposer's balance should be equal to the tx fee
			balanceProposer, err := clt.Eth().GetBalance(web3.Address(proposerAddr), web3.Latest)
			assert.NoError(t, err)

			txFee := new(big.Int).Mul(new(big.Int).SetUint64(receipt.GasUsed), txn.GasPrice)
			assert.Equalf(t, txFee, balanceProposer, "Proposer didn't get appropriate transaction fee")
		})
	}
}
