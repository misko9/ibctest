package ibc_test

import (
	"context"
	"fmt"
	"testing"

	transfertypes "github.com/cosmos/ibc-go/v5/modules/apps/transfer/types"
	"github.com/strangelove-ventures/ibctest/v5"
	"github.com/strangelove-ventures/ibctest/v5/chain/cosmos"
	"github.com/strangelove-ventures/ibctest/v5/ibc"
	"github.com/strangelove-ventures/ibctest/v5/testreporter"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestPacketForwardMiddleware(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	client, network := ibctest.DockerSetup(t)

	rep := testreporter.NewNopReporter()
	eRep := rep.RelayerExecReporter(t)

	ctx := context.Background()

	cf := ibctest.NewBuiltinChainFactory(zaptest.NewLogger(t), []*ibctest.ChainSpec{
		{Name: "gaia", Version: "strangelove-packet-forward-middleware-no-retry-default-timeout"},
		{Name: "osmosis", Version: "v11.0.1"},
		{Name: "juno", Version: "v9.0.0"},
	})

	chains, err := cf.Chains(t.Name())
	require.NoError(t, err)

	gaia, osmosis, juno := chains[0].(*cosmos.CosmosChain), chains[1].(*cosmos.CosmosChain), chains[2].(*cosmos.CosmosChain)

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
	receiver := fmt.Sprintf("%s|%s/%s:%s", gaiaUser.Bech32Address(gaia.Config().Bech32Prefix), gaiaJunoChan.PortID, gaiaJunoChan.ChannelID, junoUser.Bech32Address(juno.Config().Bech32Prefix))
	transfer := ibc.WalletAmount{
		Address: receiver,
		Denom:   osmosis.Config().Denom,
		Amount:  transferAmount,
	}

	osmosisGaiaChan := osmoChannels[0]
	_, err = osmosis.SendIBCTransfer(ctx, osmosisGaiaChan.ChannelID, osmosisUser.KeyName, transfer, nil)
	require.NoError(t, err)

	// Compose the prefixed denoms and ibc denom for asserting balances
	gaiaOsmoChan := osmoChannels[0].Counterparty
	junoGaiaChan := junoChannels[0]
	firstHopDenom := transfertypes.GetPrefixedDenom(gaiaOsmoChan.PortID, gaiaOsmoChan.ChannelID, osmosis.Config().Denom)
	secondHopDenom := transfertypes.GetPrefixedDenom(junoGaiaChan.Counterparty.PortID, junoGaiaChan.Counterparty.ChannelID, firstHopDenom)
	dstIbcDenom := transfertypes.ParseDenomTrace(secondHopDenom)

	// Check that the funds sent are gone from the acc on osmosis
	err = cosmos.PollForBalance(ctx, osmosis, 2, ibc.WalletAmount{
		Address: osmosisUser.Bech32Address(osmosis.Config().Bech32Prefix),
		Denom:   osmosis.Config().Denom,
		Amount:  userFunds - transferAmount,
	})
	require.NoError(t, err)

	// Check that the funds sent are present in the acc on juno
	err = cosmos.PollForBalance(ctx, juno, 15, ibc.WalletAmount{
		Address: junoUser.Bech32Address(juno.Config().Bech32Prefix),
		Denom:   dstIbcDenom.IBCDenom(),
		Amount:  transferAmount,
	})
	require.NoError(t, err)

	// Send packet back from Juno->Hub->Osmosis
	receiver = fmt.Sprintf("%s|%s/%s:%s", gaiaUser.Bech32Address(gaia.Config().Bech32Prefix), gaiaOsmoChan.PortID, gaiaOsmoChan.ChannelID, osmosisUser.Bech32Address(osmosis.Config().Bech32Prefix))
	transfer = ibc.WalletAmount{
		Address: receiver,
		Denom:   dstIbcDenom.IBCDenom(),
		Amount:  transferAmount,
	}

	_, err = juno.SendIBCTransfer(ctx, junoGaiaChan.ChannelID, junoUser.KeyName, transfer, nil)
	require.NoError(t, err)

	// Check that the funds sent are gone from the acc on juno
	err = cosmos.PollForBalance(ctx, juno, 2, ibc.WalletAmount{
		Address: junoUser.Bech32Address(juno.Config().Bech32Prefix),
		Denom:   dstIbcDenom.IBCDenom(),
		Amount:  int64(0),
	})
	require.NoError(t, err)

	// Check that the funds sent are present in the acc on osmosis
	err = cosmos.PollForBalance(ctx, osmosis, 15, ibc.WalletAmount{
		Address: osmosisUser.Bech32Address(osmosis.Config().Bech32Prefix),
		Denom:   osmosis.Config().Denom,
		Amount:  userFunds,
	})
	require.NoError(t, err)

	// Send a malformed packet with invalid receiver address from Osmosis->Hub->Juno
	// This should succeed in the first hop and fail to make the second hop; funds should then be refunded to osmosis.
	receiver = fmt.Sprintf("%s|%s/%s:%s", gaiaUser.Bech32Address(gaia.Config().Bech32Prefix), gaiaJunoChan.PortID, gaiaJunoChan.ChannelID, "xyz1t8eh66t2w5k67kwurmn5gqhtq6d2ja0vp7jmmq")
	transfer = ibc.WalletAmount{
		Address: receiver,
		Denom:   osmosis.Config().Denom,
		Amount:  transferAmount,
	}

	_, err = osmosis.SendIBCTransfer(ctx, osmosisGaiaChan.ChannelID, osmosisUser.KeyName, transfer, nil)
	require.NoError(t, err)

	// Wait until the funds sent are gone from the acc on osmosis
	err = cosmos.PollForBalance(ctx, osmosis, 2, ibc.WalletAmount{
		Address: osmosisUser.Bech32Address(osmosis.Config().Bech32Prefix),
		Denom:   osmosis.Config().Denom,
		Amount:  userFunds - transferAmount,
	})
	require.NoError(t, err)

	// Wait until the funds sent are back in the acc on osmosis
	err = cosmos.PollForBalance(ctx, osmosis, 15, ibc.WalletAmount{
		Address: osmosisUser.Bech32Address(osmosis.Config().Bech32Prefix),
		Denom:   osmosis.Config().Denom,
		Amount:  userFunds,
	})
	require.NoError(t, err)

	// Check that the gaia account is empty
	intermediaryIBCDenom := transfertypes.ParseDenomTrace(firstHopDenom)
	gaiaBal, err := gaia.GetBalance(ctx, gaiaUser.Bech32Address(gaia.Config().Bech32Prefix), intermediaryIBCDenom.IBCDenom())
	require.NoError(t, err)
	require.Equal(t, int64(0), gaiaBal)

	// Now restart all nodes with a version of gaia that has 2 retries and 1 second timeout
	err = gaia.StopAllNodes(ctx)
	require.NoError(t, err)
	gaia.UpgradeVersion(ctx, client, "strangelove-packet-forward-middleware-two-retries-low-timeout")
	err = gaia.StartAllNodes(ctx)
	require.NoError(t, err)

	// Send packet from Osmosis->Hub->Juno with the timeout so low that it can not make it from Hub to Juno, which should result in a refund from Hub to Osmosis after two retries.
	// receiver format: {intermediate_refund_address}|{foward_port}/{forward_channel}:{final_destination_address}:{max_retries}:{timeout_duration}
	receiver = fmt.Sprintf("%s|%s/%s:%s", gaiaUser.Bech32Address(gaia.Config().Bech32Prefix), gaiaJunoChan.PortID, gaiaJunoChan.ChannelID, junoUser.Bech32Address(juno.Config().Bech32Prefix))
	transfer = ibc.WalletAmount{
		Address: receiver,
		Denom:   osmosis.Config().Denom,
		Amount:  transferAmount,
	}

	_, err = osmosis.SendIBCTransfer(ctx, osmosisGaiaChan.ChannelID, osmosisUser.KeyName, transfer, nil)
	require.NoError(t, err)

	// Wait until the funds sent are gone from the acc on osmosis
	err = cosmos.PollForBalance(ctx, osmosis, 2, ibc.WalletAmount{
		Address: osmosisUser.Bech32Address(osmosis.Config().Bech32Prefix),
		Denom:   osmosis.Config().Denom,
		Amount:  userFunds - transferAmount,
	})
	require.NoError(t, err)

	// Wait until the funds leave the gaia wallet (attempting to send to juno)
	err = cosmos.PollForBalance(ctx, gaia, 5, ibc.WalletAmount{
		Address: gaiaUser.Bech32Address(gaia.Config().Bech32Prefix),
		Denom:   intermediaryIBCDenom.IBCDenom(),
		Amount:  0,
	})
	require.NoError(t, err)

	// Wait until the funds are back in the acc on osmosis
	err = cosmos.PollForBalance(ctx, osmosis, 15, ibc.WalletAmount{
		Address: osmosisUser.Bech32Address(osmosis.Config().Bech32Prefix),
		Denom:   osmosis.Config().Denom,
		Amount:  userFunds,
	})
	require.NoError(t, err)
}
