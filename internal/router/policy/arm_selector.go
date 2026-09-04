package policy

import (
	"context"

	"weave-os/router/internal/router"
)

// SelectionInput is the content-free classification the router selects an arm from.
type SelectionInput struct {
	Strategy           router.Strategy
	ExecutionMode      string
	RouteID            string
	Harness            string
	ClassifierGroup    string
	RankedFallback     []PreviewGroup
	CandidateRosterIDs []string
}

// SelectionPick is the router's selected arm.
type SelectionPick struct {
	Group string
	Arm   string
}

// ArmSelector picks the served arm from a sidecar classification. An error
// fails the turn: with a classifier-only sidecar there is no arm to fall back to.
type ArmSelector func(ctx context.Context, input SelectionInput) (SelectionPick, error)

// selectionInputFor snapshots the sidecar's classification for the arm selector.
func selectionInputFor(strategy router.Strategy, executionMode string, req router.Request, res Result, resolved ResolvedCandidates) SelectionInput {
	candidateRosterIDs := make([]string, 0, len(resolved.Candidates))
	for _, candidate := range resolved.Candidates {
		candidateRosterIDs = append(candidateRosterIDs, candidate.RosterID)
	}
	return SelectionInput{
		Strategy:           strategy,
		ExecutionMode:      executionMode,
		RouteID:            res.RouteID,
		Harness:            req.ClientApp,
		ClassifierGroup:    res.PolicyGroup,
		RankedFallback:     res.RankedFallback,
		CandidateRosterIDs: candidateRosterIDs,
	}
}
