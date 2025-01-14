// Package mevcommitclient provides functionality for interacting with the mev-commit protocol.
// It includes interfaces and implementations for querying validator opt-in status,
// monitoring builder events, and validating builder registrations.
package mevcommitclient

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	providerRegistry "github.com/primev/mev-commit/contracts-abi/clients/ProviderRegistry"
	validatoroptinrouter "github.com/primev/mev-commit/contracts-abi/clients/ValidatorOptInRouter"
)

type MevCommitProvider struct {
	Pubkey     []byte
	EOAAddress common.Address
}

type IMevCommitClient interface {
	GetOptInStatusForValidators(pubkeys []string) ([]bool, error)
	ListenForBuildersEvents() (<-chan MevCommitProvider, <-chan common.Address, error)
	IsBuilderValid(builderAddress common.Address) (bool, error)
}

type MevCommitClient struct {
	L1Address                  string
	MevCommitAddress           string
	ValidatorRouterAddress     common.Address
	ProviderRegistryAddress    common.Address
	validatorOptInRouterCaller *validatoroptinrouter.ValidatoroptinrouterCaller
	builderRegistryCaller      *providerRegistry.ProviderregistryCaller
	builderRegistryFilterer    *providerRegistry.ProviderregistryFilterer
	l1Client                   *ethclient.Client
	mevCommitClient            *ethclient.Client
	contractAbi                abi.ABI
}

const (
	abiJSON = `[{"anonymous":false,"inputs":[{"indexed":true,"internalType":"address","name":"builder","type":"address"},{"indexed":false,"internalType":"uint256","name":"value","type":"uint256"},{"indexed":false,"internalType":"bytes","name":"blsPublicKey","type":"bytes"}],"name":"BuilderRegistered","type":"event"},{"inputs":[{"internalType":"address","name":"builder","type":"address"}],"name":"isBuilderValid","outputs":[],"stateMutability":"view","type":"function"}]`
)

func NewMevCommitClient(l1MainnetURL, mevCommitURL string, validatorRouterAddress, ProviderRegistryAddress common.Address) (IMevCommitClient, error) {
	l1Client, err := ethclient.Dial(l1MainnetURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to L1 Mainnet: %w", err)
	}
	var mevCommitClient *ethclient.Client
	if strings.HasPrefix(mevCommitURL, "ws") {
		mevCommitClient, err = ethclient.DialContext(context.Background(), mevCommitURL)
	} else {
		mevCommitClient, err = ethclient.Dial(mevCommitURL)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mev-commit EVM: %w", err)
	}

	validatorOptInRouter, err := validatoroptinrouter.NewValidatoroptinrouterCaller(validatorRouterAddress, l1Client)
	if err != nil {
		return nil, fmt.Errorf("failed to create ValidatorOptInRouter caller: %w", err)
	}

	builderRegistryCaller, err := providerRegistry.NewProviderregistryCaller(ProviderRegistryAddress, mevCommitClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create BuilderRegistry caller: %w", err)
	}

	builderRegistryFilterer, err := providerRegistry.NewProviderregistryFilterer(ProviderRegistryAddress, mevCommitClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create BuilderRegistry filterer: %w", err)
	}

	contractAbi, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to parse contract ABI: %w", err)
	}

	return &MevCommitClient{
		L1Address:                  l1MainnetURL,
		MevCommitAddress:           mevCommitURL,
		ValidatorRouterAddress:     validatorRouterAddress,
		ProviderRegistryAddress:    ProviderRegistryAddress,
		validatorOptInRouterCaller: validatorOptInRouter,
		builderRegistryCaller:      builderRegistryCaller,
		builderRegistryFilterer:    builderRegistryFilterer,
		l1Client:                   l1Client,
		mevCommitClient:            mevCommitClient,
		contractAbi:                contractAbi,
	}, nil
}

func (m *MevCommitClient) GetOptInStatusForValidators(pubkeys []string) ([]bool, error) {
	// Get the finalized block number
	currentBlockNumber, err := m.l1Client.BlockNumber(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get current block number: %w", err)
	}

	opts := &bind.CallOpts{
		BlockNumber: big.NewInt(int64(currentBlockNumber - 64)),
	}
	pubkeysBytes := make([][]byte, len(pubkeys))
	for i, pubkey := range pubkeys {
		pubkeysBytes[i] = common.Hex2Bytes(strings.TrimPrefix(pubkey, "0x"))
	}

	optInStatuses, err := m.validatorOptInRouterCaller.AreValidatorsOptedIn(opts, pubkeysBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to get opt-in status: %w", err)
	}

	isOptedIn := make([]bool, len(optInStatuses))
	for i, status := range optInStatuses {
		isOptedIn[i] = status.IsAvsOptedIn || status.IsVanillaOptedIn || status.IsMiddlewareOptedIn
	}

	return isOptedIn, nil
}

const blockRangeSize = 5000

func (m *MevCommitClient) ListenForBuildersEvents() (<-chan MevCommitProvider, <-chan common.Address, error) {
	builderRegistryEventCh := make(chan MevCommitProvider)
	builderUnregisteredEventCh := make(chan common.Address)

	// Create a context with cancellation for cleanup
	ctx, cancel := context.WithCancel(context.Background())

	// Start a polling goroutine
	go func() {
		defer close(builderRegistryEventCh)
		defer close(builderUnregisteredEventCh)
		defer cancel()

		backoff := time.Second
		maxBackoff := time.Minute * 2

		// Start from block 0
		lastProcessedBlock := uint64(0)
		var processingError bool

		for {
			select {
			case <-ctx.Done():
				return
			default:
				if err := m.filterEvents(ctx, builderRegistryEventCh, builderUnregisteredEventCh, lastProcessedBlock); err != nil {
					fmt.Printf("Filter error: %v\n", err)
					// Mark that we had an error so we reprocess this chunk
					processingError = true
					// Exponential backoff with jitter
					jitter := time.Duration(rand.Float64() * float64(backoff/4))
					time.Sleep(backoff + jitter)
					backoff = time.Duration(math.Min(float64(backoff*2), float64(maxBackoff)))
					continue
				}

				// Only advance the block range if we successfully processed without errors
				if !processingError {
					lastProcessedBlock += blockRangeSize
				}
				processingError = false

				// Get current block to ensure we don't process future blocks
				currentBlock, err := m.l1Client.BlockNumber(context.Background())
				if err != nil {
					fmt.Printf("Failed to get current block number: %v\n", err)
					processingError = true
					continue
				}

				// If we've caught up to the current block, wait before polling again
				if lastProcessedBlock >= currentBlock {
					lastProcessedBlock = currentBlock - blockRangeSize // Roll back to reprocess last chunk
					time.Sleep(time.Second * 12)                       // Roughly one block time
				}

				// Reset backoff on successful filtering
				backoff = time.Second
			}
		}
	}()

	return builderRegistryEventCh, builderUnregisteredEventCh, nil
}

func (m *MevCommitClient) filterEvents(ctx context.Context, builderRegistryEventCh chan MevCommitProvider, builderUnregisteredEventCh chan common.Address, fromBlock uint64) error {
	// Calculate block range
	toBlock := fromBlock + blockRangeSize
	currentBlock, err := m.l1Client.BlockNumber(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get current block number: %w", err)
	}
	if toBlock > currentBlock {
		toBlock = currentBlock
	}

	filterOpts := &bind.FilterOpts{
		Start:   fromBlock,
		End:     &toBlock,
		Context: ctx,
	}

	// Filter BLSKeyAdded events
	blsKeyEvents, err := m.builderRegistryFilterer.FilterBLSKeyAdded(filterOpts, nil)
	if err != nil {
		return fmt.Errorf("failed to filter BLSKeyAdded events: %w", err)
	}
	defer blsKeyEvents.Close()

	// Filter FundsSlashed events
	fundsSlashedEvents, err := m.builderRegistryFilterer.FilterFundsSlashed(filterOpts, nil)
	if err != nil {
		return fmt.Errorf("failed to filter FundsSlashed events: %w", err)
	}
	defer fundsSlashedEvents.Close()

	// Process BLSKeyAdded events
	for blsKeyEvents.Next() {
		select {
		case <-ctx.Done():
			return nil
		default:
			builderRegistryEventCh <- MevCommitProvider{
				Pubkey:     blsKeyEvents.Event.BlsPublicKey,
				EOAAddress: blsKeyEvents.Event.Provider,
			}
		}
	}

	// Process FundsSlashed events
	for fundsSlashedEvents.Next() {
		select {
		case <-ctx.Done():
			return nil
		default:
			isValid, err := m.IsBuilderValid(fundsSlashedEvents.Event.Provider)
			if err != nil {
				fmt.Printf("failed to check if builder is valid: %v\n", err)
				continue
			}
			if !isValid {
				builderUnregisteredEventCh <- fundsSlashedEvents.Event.Provider
			}
		}
	}

	return nil
}

func (m *MevCommitClient) IsBuilderValid(builderAddress common.Address) (bool, error) {
	err := m.builderRegistryCaller.IsProviderValid(nil, builderAddress)
	if err != nil {
		if err.Error() == "execution reverted" {
			return false, nil
		}
		return false, fmt.Errorf("error checking if builder is valid: %w", err)
	}
	return true, nil
}
