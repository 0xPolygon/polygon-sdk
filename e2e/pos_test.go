package e2e

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/0xPolygon/minimal/crypto"
	"github.com/0xPolygon/minimal/e2e/framework"
	"github.com/0xPolygon/minimal/types"
	"github.com/stretchr/testify/assert"
)

func TestPoS_Stake(t *testing.T) {
	signer := &crypto.FrontierSigner{}
	stakerKey, stakerAddr := framework.GenerateKeyAndAddr(t)
	stakingContractAddr := types.StringToAddress("1001")

	dataDir, err := framework.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	ibftManager := framework.NewIBFTServersManager(t, IBFTMinNodes, dataDir, IBFTDirPrefix, func(i int, config *framework.TestServerConfig) {
		config.Premine(stakerAddr, framework.EthToWei(10))
		config.SetSeal(true)
		config.SetShowsLog(i == 0)
	})
	t.Cleanup(func() {
		if err := os.RemoveAll(dataDir); err != nil {
			t.Log(err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ibftManager.StartServers(ctx)

	srv := ibftManager.GetServer(0)

	// Stake Balance
	txn := &types.Transaction{
		From:     stakerAddr,
		To:       &stakingContractAddr,
		GasPrice: big.NewInt(10000),
		Gas:      1000000,
		Value:    framework.EthToWei(1),
		Nonce:    0,
	}
	txn, err = signer.SignTx(txn, stakerKey)
	if err != nil {
		t.Fatal(err)
	}
	data := txn.MarshalRLP()
	hash, err := srv.JSONRPC().Eth().SendRawTransaction(data)
	assert.NoError(t, err)
	assert.NotNil(t, hash)

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	receipt, err := srv.WaitForReceipt(ctx, hash)
	assert.NoError(t, err)
	assert.NotNil(t, receipt)

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// staker will join from next block
	snapshot, err := srv.WaitForIBFTSnapshot(ctx, receipt.BlockNumber+1)
	assert.NoError(t, err)
	assert.NotNil(t, snapshot)

	found := false
	for _, v := range snapshot.Validators {
		if v.Address == stakerAddr.String() {
			found = true
			break
		}
	}
	assert.True(t, found, "staker should join to validator set, but didn't")
}