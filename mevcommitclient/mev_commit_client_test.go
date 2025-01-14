package mevcommitclient

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	mevCommitRPC             = "wss://chainrpc-wss.testnet.mev-commit.xyz"
	ethereumL1RPC            = "https://ethereum-holesky-rpc.publicnode.com"
	providerRegistryAddr     = "0x1C2a592950E5dAd49c0E2F3A402DCF496bdf7b67"
	validatorOptInRouterAddr = "0x251Fbc993f58cBfDA8Ad7b0278084F915aCE7fc3"
)

func TestNewMevCommitClient(t *testing.T) {
	client, err := NewMevCommitClient(
		ethereumL1RPC,
		mevCommitRPC,
		common.HexToAddress(validatorOptInRouterAddr),
		common.HexToAddress(providerRegistryAddr),
	)
	require.NoError(t, err)
	require.NotNil(t, client)

	mevClient, ok := client.(*MevCommitClient)
	require.True(t, ok)

	assert.Equal(t, ethereumL1RPC, mevClient.L1Address)
	assert.Equal(t, mevCommitRPC, mevClient.MevCommitAddress)
	assert.Equal(t, common.HexToAddress(validatorOptInRouterAddr), mevClient.ValidatorRouterAddress)
	assert.Equal(t, common.HexToAddress(providerRegistryAddr), mevClient.ProviderRegistryAddress)
}

func TestGetOptInStatusForValidators(t *testing.T) {
	client, err := NewMevCommitClient(
		ethereumL1RPC,
		mevCommitRPC,
		common.HexToAddress(validatorOptInRouterAddr),
		common.HexToAddress(providerRegistryAddr),
	)
	require.NoError(t, err)
	// Test with some sample public keys
	pubkeys := []string{
		"010203",
		"040506",
	}

	statuses, err := client.GetOptInStatusForValidators(pubkeys)
	require.NoError(t, err)
	assert.Len(t, statuses, len(pubkeys))

	// Note: The actual values will depend on the state of the contract
	// This test just checks that we get a response without error
	for _, status := range statuses {
		assert.IsType(t, bool(true), status)
	}
}
func TestListenForBuildersEvents(t *testing.T) {
	client, err := NewMevCommitClient(
		ethereumL1RPC,
		mevCommitRPC,
		common.HexToAddress(validatorOptInRouterAddr),
		common.HexToAddress(providerRegistryAddr),
	)
	require.NoError(t, err)

	builderRegisteredCh, builderUnregisteredCh, err := client.ListenForBuildersEvents()
	require.NoError(t, err)

	// Create a channel to signal when we receive an event
	eventReceived := make(chan struct{})

	go func() {
		select {
		case builder := <-builderRegisteredCh:
			t.Logf("Builder registered - Address: %v", builder)
			eventReceived <- struct{}{}
		case address := <-builderUnregisteredCh:
			t.Logf("Builder unregistered - Address: %v", address)
			eventReceived <- struct{}{}
		case <-time.After(10 * time.Second):
			t.Error("Timeout waiting for builder event")
			eventReceived <- struct{}{}
		}
	}()

	// Wait for one event
	<-eventReceived
}

func TestGetOptInStatusForSpecificValidator(t *testing.T) {
	client, err := NewMevCommitClient(
		ethereumL1RPC,
		mevCommitRPC,
		common.HexToAddress(validatorOptInRouterAddr),
		common.HexToAddress(providerRegistryAddr),
	)
	require.NoError(t, err)

	// Specific validator public key we know is opted in
	pubkey := "0xa7884bb9b06b912ec80d14e408cd88282f813547082b7a86bc1dd9c1881e29a781314f1f9108d6059a7ec10852e14028"
	statuses, err := client.GetOptInStatusForValidators([]string{pubkey})
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0], "Expected opt-in status to be true")

	pubkeyWithoutPrefix := "a7884bb9b06b912ec80d14e408cd88282f813547082b7a86bc1dd9c1881e29a781314f1f9108d6059a7ec10852e14028"
	statuses, err = client.GetOptInStatusForValidators([]string{pubkeyWithoutPrefix})
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0], "Expected opt-in status to be true")

}
