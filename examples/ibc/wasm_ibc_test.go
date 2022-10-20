package ibc_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	transfertypes "github.com/cosmos/ibc-go/v6/modules/apps/transfer/types"
	"github.com/strangelove-ventures/ibctest/v6"
//	"github.com/strangelove-ventures/ibctest/v6/chain/cosmos"
	"github.com/strangelove-ventures/ibctest/v6/ibc"
	"github.com/strangelove-ventures/ibctest/v6/test"
	"github.com/strangelove-ventures/ibctest/v6/testreporter"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// This test is meant to be used as a basic ibctest tutorial.
// Code snippets are broken down in ./docs/upAndRunning.md
func TestWasmIbc(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	ctx := context.Background()

	// Chain Factory
	cf := ibctest.NewBuiltinChainFactory(zaptest.NewLogger(t), []*ibctest.ChainSpec{
		{Name: "juno", ChainName: "juno1", Version: "latest", ChainConfig: ibc.ChainConfig{GasPrices:  "0.00ujuno"}},
		{Name: "juno", ChainName: "juno2", Version: "latest", ChainConfig: ibc.ChainConfig{GasPrices:  "0.00ujuno"}},
		/*{ChainConfig: ibc.ChainConfig{
			Type: "cosmos",
			Name: "juno",
			ChainID: "juno1",
			Images: []ibc.DockerImage{
				{
					Repository: "juno",
					Version: "v11.0.0-alpha",
            		UidGid: "1025:1025",
				},
			},
			Bin: "junod",
			Bech32Prefix: "juno",
			Denom: "ujuno",
			GasPrices: "0.00ujuno",
			GasAdjustment: 1.3,
			TrustingPeriod: "504h",
			NoHostMount: true,
		}},
		{ChainConfig: ibc.ChainConfig{
			Type: "cosmos",
			Name: "juno",
			ChainID: "juno2",
			Images: []ibc.DockerImage{
				{
					Repository: "juno",
					Version: "v11.0.0-alpha",
            		UidGid: "1025:1025",
				},
			},
			Bin: "junod",
			Bech32Prefix: "juno",
			Denom: "ujuno",
			GasPrices: "0.00ujuno",
			GasAdjustment: 1.3,
			TrustingPeriod: "504h",
			NoHostMount: true,
		}},*/
	})

	chains, err := cf.Chains(t.Name())
	require.NoError(t, err)
	juno1, juno2 := chains[0], chains[1]

	// Relayer Factory
	client, network := ibctest.DockerSetup(t)
	r := ibctest.NewBuiltinRelayerFactory(ibc.CosmosRly, zaptest.NewLogger(t)).Build(
		t, client, network)

	// Prep Interchain
	const ibcPath = "wasm-ibc-test"
	ic := ibctest.NewInterchain().
		AddChain(juno1).
		AddChain(juno2).
		AddRelayer(r, "relayer").
		AddLink(ibctest.InterchainLink{
			Chain1:  juno1,
			Chain2:  juno2,
			Relayer: r,
			Path:    ibcPath,
		})

	// Log location
	f, err := ibctest.CreateLogFile(fmt.Sprintf("wasm_ibc_test_%d.json", time.Now().Unix()))
	require.NoError(t, err)
	// Reporter/logs
	rep := testreporter.NewReporter(f)
	eRep := rep.RelayerExecReporter(t)

	// Build interchain
	require.NoError(t, ic.Build(ctx, eRep, ibctest.InterchainBuildOptions{
		TestName:          t.Name(),
		Client:            client,
		NetworkID:         network,
		BlockDatabaseFile: ibctest.DefaultBlockDatabaseFilepath(),

		SkipPathCreation: false},
	),
	)
	t.Cleanup(func() {
		_ = ic.Close()
	})

	// Create and Fund User Wallets
	fundAmount := int64(10_000_000)
	users := ibctest.GetAndFundTestUsers(t, ctx, "default", int64(fundAmount), juno1, juno2)
	juno1User := users[0]
	juno2User := users[1]

	juno1UserBalInitial, err := juno1.GetBalance(ctx, juno1User.Bech32Address(juno1.Config().Bech32Prefix), juno1.Config().Denom)
	require.NoError(t, err)
	require.Equal(t, fundAmount, juno1UserBalInitial)

	juno2UserBalInitial, err := juno2.GetBalance(ctx, juno2User.Bech32Address(juno2.Config().Bech32Prefix), juno2.Config().Denom)
	require.NoError(t, err)
	require.Equal(t, fundAmount, juno2UserBalInitial)

	// Get Channel ID
	juno1ChannelInfo, err := r.GetChannels(ctx, eRep, juno1.Config().ChainID)
	require.NoError(t, err)
	juno1ChannelID := juno1ChannelInfo[0].ChannelID

	//juno2ChannelInfo, err := r.GetChannels(ctx, eRep, juno2.Config().ChainID)
	//require.NoError(t, err)
	//juno2ChannelID := juno2ChannelInfo[0].ChannelID

	// Start the relayer on both paths
	err = r.StartRelayer(ctx, eRep, ibcPath)
	//require.NoError(t, err)

	t.Cleanup(
		func() {
			err := r.StopRelayer(ctx, eRep)
			if err != nil {
				t.Logf("an error occured while stopping the relayer: %s", err)
			}
		},
	)

	// Send Transaction
	amountToSend := int64(1_000_000)
	dstAddress := juno2User.Bech32Address(juno2.Config().Bech32Prefix)
	tx, err := juno1.SendIBCTransfer(ctx, juno1ChannelID, juno1User.KeyName, ibc.WalletAmount{
		Address: dstAddress,
		Denom:   juno1.Config().Denom,
		Amount:  amountToSend,
	},
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, tx.Validate())

	/*juno1ContractAddr, _, err := juno1.(*cosmos.CosmosChain).InstantiateContract(
		ctx, 
		juno1User.KeyName, 
		ibc.WalletAmount{
			Address: dstAddress,
			Denom: juno1.Config().Denom,
			Amount: int64(1_000_000),
		},
		"./ibc_reflect_send.wasm",
		"{}",
		true,
	)
	require.NoError(t, err)

	_, codeId, err := juno2.(*cosmos.CosmosChain).InstantiateContract(
		ctx, 
		juno2User.KeyName, 
		ibc.WalletAmount{
			Address: dstAddress,
			Denom: juno1.Config().Denom,
			Amount: int64(1_000_000),
		},
		"./reflect.wasm",
		"{}",
		true,
	)
	require.NoError(t, err)

	initMsg := "{\"reflect_code_id\":" + codeId + "}"
	juno2IbcReflectContractAddr, _, err := juno2.(*cosmos.CosmosChain).InstantiateContract(
		ctx, 
		juno2User.KeyName, 
		ibc.WalletAmount{
			Address: dstAddress,
			Denom: juno1.Config().Denom,
			Amount: int64(1_000_000),
		},
		"./ibc_reflect.wasm",
		initMsg,
		true,
	)
	require.NoError(t, err)*/

	//err = juno1.(*cosmos.CosmosChain).ExecuteContract(ctx, juno1User.KeyName, juno1ContractAddr, "message")
	//require.NoError(t, err)

	err = test.WaitForBlocks(ctx, 5, juno1, juno2)
	require.NoError(t, err)
	// relay packets and acknoledgments
	//require.NoError(t, r.FlushPackets(ctx, eRep, ibcPath, juno2ChannelID))
	//require.NoError(t, r.FlushAcknowledgements(ctx, eRep, ibcPath, juno1ChannelID))

	// test source wallet has decreased funds
	expectedBal := juno1UserBalInitial - amountToSend
	juno1UserBalNew, err := juno1.GetBalance(ctx, juno1User.Bech32Address(juno1.Config().Bech32Prefix), juno1.Config().Denom)
	require.NoError(t, err)
	require.Equal(t, expectedBal, juno1UserBalNew)

	// Trace IBC Denom
	srcDenomTrace := transfertypes.ParseDenomTrace(transfertypes.GetPrefixedDenom("transfer", juno1ChannelID, juno1.Config().Denom))
	dstIbcDenom := srcDenomTrace.IBCDenom()

	// Test destination wallet has increased funds
	juno2UserBalNew, err := juno2.GetBalance(ctx, juno2User.Bech32Address(juno2.Config().Bech32Prefix), dstIbcDenom)
	require.NoError(t, err)
	require.Equal(t, amountToSend, juno2UserBalNew)
}
