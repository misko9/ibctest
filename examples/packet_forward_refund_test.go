package ibctest_test

import (
	"context"
	"fmt"
	"testing"

	transfertypes "github.com/cosmos/ibc-go/v5/modules/apps/transfer/types"
	"github.com/strangelove-ventures/ibctest/v5"
	"github.com/strangelove-ventures/ibctest/v5/ibc"
	"github.com/strangelove-ventures/ibctest/v5/test"
	"github.com/strangelove-ventures/ibctest/v5/testreporter"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestPacketForwardMiddlewareRefund(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	client, network := ibctest.DockerSetup(t)

	rep := testreporter.NewNopReporter()
	eRep := rep.RelayerExecReporter(t)

	ctx := context.Background()

	cf := ibctest.NewBuiltinChainFactory(zaptest.NewLogger(t), []*ibctest.ChainSpec{
		{Name: "gaia", Version: "andrew-packet_forward_middleware"},
		{Name: "osmosis", Version: "v11.0.1"},
		{Name: "juno", Version: "v9.0.0"},
	})

	chains, err := cf.Chains(t.Name())
	require.NoError(t, err)

	gaia, osmosis, juno := chains[0], chains[1], chains[2]

	r := ibctest.NewBuiltinRelayerFactory(
		ibc.CosmosRly,
		zaptest.NewLogger(t),
	).Build(
		t, client, network,
	)

	const pathOsmoHub = "osmohub"
	const pathJunoHub = "junohub"

	ic := ibctest.NewInterchain().
		AddChain(osmosis).
		AddChain(gaia).
		AddChain(juno).
		AddRelayer(r, "relayer").
		AddLink(ibctest.InterchainLink{
			Chain1:  osmosis,
			Chain2:  gaia,
			Relayer: r,
			Path:    pathOsmoHub,
		}).
		AddLink(ibctest.InterchainLink{
			Chain1:  gaia,
			Chain2:  juno,
			Relayer: r,
			Path:    pathJunoHub,
		})

	require.NoError(t, ic.Build(ctx, eRep, ibctest.InterchainBuildOptions{
		TestName:          t.Name(),
		Client:            client,
		NetworkID:         network,
		BlockDatabaseFile: ibctest.DefaultBlockDatabaseFilepath(),

		SkipPathCreation: false,
	}))
	t.Cleanup(func() {
		_ = ic.Close()
	})

	const userFunds = int64(10_000_000_000)
	users := ibctest.GetAndFundTestUsers(t, ctx, t.Name(), userFunds, osmosis, gaia, juno)

	osmoChannels, err := r.GetChannels(ctx, eRep, osmosis.Config().ChainID)
	require.NoError(t, err)

	junoChannels, err := r.GetChannels(ctx, eRep, juno.Config().ChainID)
	require.NoError(t, err)

	// Start the relayer on both paths
	err = r.StartRelayer(ctx, eRep, pathOsmoHub, pathJunoHub)
	require.NoError(t, err)

	t.Cleanup(
		func() {
			err := r.StopRelayer(ctx, eRep)
			if err != nil {
				t.Logf("an error occured while stopping the relayer: %s", err)
			}
		},
	)

	// Get original account balances
	osmosisUser, gaiaUser, junoUser := users[0], users[1], users[2]

	// Send packet from Osmosis->Hub->Juno
	// receiver format: {intermediate_refund_address}|{foward_port}/{forward_channel}:{final_destination_address}
	const transferAmount int64 = 100000
	gaiaJunoChan := junoChannels[0].Counterparty

	osmosisGaiaChan := osmoChannels[0]

	// Compose the prefixed denoms and ibc denom for asserting balances
	gaiaOsmoChan := osmoChannels[0].Counterparty
	junoGaiaChan := junoChannels[0]
	firstHopDenom := transfertypes.GetPrefixedDenom(gaiaOsmoChan.PortID, gaiaOsmoChan.ChannelID, osmosis.Config().Denom)
	secondHopDenom := transfertypes.GetPrefixedDenom(junoGaiaChan.Counterparty.PortID, junoGaiaChan.Counterparty.ChannelID, firstHopDenom)
	intermediaryIBCDenom := transfertypes.ParseDenomTrace(firstHopDenom)
	dstIbcDenom := transfertypes.ParseDenomTrace(secondHopDenom)

	osmosisBal, err := osmosis.GetBalance(ctx, osmosisUser.Bech32Address(osmosis.Config().Bech32Prefix), osmosis.Config().Denom)
	require.NoError(t, err)

	gaiaBal, err := gaia.GetBalance(ctx, gaiaUser.Bech32Address(gaia.Config().Bech32Prefix), intermediaryIBCDenom.IBCDenom())
	require.NoError(t, err)

	junoBal, err := juno.GetBalance(ctx, junoUser.Bech32Address(juno.Config().Bech32Prefix), dstIbcDenom.IBCDenom())
	require.NoError(t, err)

	// Send packet from Osmosis->Hub->Juno with the timeout so low that it can not make it from Hub to Juno, which should result in a refund from Hub to Osmosis.
	// receiver format: {intermediate_refund_address}|{foward_port}/{forward_channel}:{final_destination_address}:{max_retries}:{timeout_duration}
	receiver := fmt.Sprintf("%s|%s/%s:%s:%d:%s", gaiaUser.Bech32Address(gaia.Config().Bech32Prefix), gaiaJunoChan.PortID, gaiaJunoChan.ChannelID, junoUser.Bech32Address(juno.Config().Bech32Prefix), 2, "1s")
	transfer := ibc.WalletAmount{
		Address: receiver,
		Denom:   osmosis.Config().Denom,
		Amount:  transferAmount,
	}

	_, err = osmosis.SendIBCTransfer(ctx, osmosisGaiaChan.ChannelID, osmosisUser.KeyName, transfer, nil)
	require.NoError(t, err)

	// Wait for transfer to be relayed
	err = test.WaitForBlocks(ctx, 50, gaia, juno, osmosis)
	require.NoError(t, err)

	// Check that the balances are the same as before

	osmosisBalPostRefund, err := osmosis.GetBalance(ctx, osmosisUser.Bech32Address(osmosis.Config().Bech32Prefix), osmosis.Config().Denom)
	require.NoError(t, err)
	require.Equal(t, osmosisBal, osmosisBalPostRefund)

	gaiaBalPostRefund, err := gaia.GetBalance(ctx, gaiaUser.Bech32Address(gaia.Config().Bech32Prefix), intermediaryIBCDenom.IBCDenom())
	require.NoError(t, err)
	require.Equal(t, gaiaBal, gaiaBalPostRefund)

	// Check that the funds sent are present in the acc on juno
	junoBalPostRefund, err := juno.GetBalance(ctx, junoUser.Bech32Address(juno.Config().Bech32Prefix), dstIbcDenom.IBCDenom())
	require.NoError(t, err)
	require.Equal(t, junoBal, junoBalPostRefund)

}
