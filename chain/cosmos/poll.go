package cosmos

import (
	"context"
	"fmt"

	"github.com/strangelove-ventures/ibctest/v5/ibc"
	"github.com/strangelove-ventures/ibctest/v5/test"
)

// PollForProposalStatus attempts to find a proposal with matching ID and status.
func PollForProposalStatus(ctx context.Context, chain *CosmosChain, startHeight, maxHeight uint64, proposalID string, status string) (ProposalResponse, error) {
	var zero ProposalResponse
	doPoll := func(ctx context.Context, height uint64) (any, error) {
		p, err := chain.QueryProposal(ctx, proposalID)
		if err != nil {
			return zero, err
		}
		if p.Status != status {
			return zero, fmt.Errorf("proposal status (%s) does not match expected: (%s)", p.Status, status)
		}
		return *p, nil
	}
	bp := test.BlockPoller{CurrentHeight: chain.Height, PollFunc: doPoll}
	p, err := bp.DoPoll(ctx, startHeight, maxHeight)
	if err != nil {
		return zero, err
	}
	return p.(ProposalResponse), nil
}

// PollForBalance polls until the balance matches
func PollForBalance(ctx context.Context, chain *CosmosChain, deltaBlocks uint64, balance ibc.WalletAmount) error {
	h, err := chain.Height(ctx)
	if err != nil {
		return fmt.Errorf("failed to get height: %w", err)
	}
	doPoll := func(ctx context.Context, height uint64) (any, error) {
		bal, err := chain.GetBalance(ctx, balance.Address, balance.Denom)
		if err != nil {
			return nil, err
		}
		if bal != balance.Amount {
			return nil, fmt.Errorf("balance (%d) does not match expected: (%d)", bal, balance.Amount)
		}
		return nil, nil
	}
	bp := test.BlockPoller{CurrentHeight: chain.Height, PollFunc: doPoll}
	_, err = bp.DoPoll(ctx, h, h+deltaBlocks)
	return err
}
