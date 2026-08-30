package policy

import (
	"context"

	"workweave/router/internal/router"
)

// SelectionObservation is the content-free record of one completed sidecar decision.
type SelectionObservation struct {
	Strategy           router.Strategy
	ExecutionMode      string
	RouteID            string
	Harness            string
	SidecarGroup       string
	SidecarPick        string
	RankedFallback     []PreviewGroup
	CandidateRosterIDs []string
}

// SelectionPick is an authoritative re-selection of the served arm.
type SelectionPick struct {
	Group string
	Arm   string
}

// SelectionOverride recomputes the served arm from a completed sidecar
// decision. Returning ok=false leaves the sidecar's pick untouched.
type SelectionOverride func(ctx context.Context, observation SelectionObservation) (SelectionPick, bool)

// selectionObservationFor snapshots the sidecar's pre-override decision for
// the selection override.
func selectionObservationFor(strategy router.Strategy, executionMode string, req router.Request, res Result, resolved ResolvedCandidates) SelectionObservation {
	candidateRosterIDs := make([]string, 0, len(resolved.Candidates))
	for _, candidate := range resolved.Candidates {
		candidateRosterIDs = append(candidateRosterIDs, candidate.RosterID)
	}
	return SelectionObservation{
		Strategy:           strategy,
		ExecutionMode:      executionMode,
		RouteID:            res.RouteID,
		Harness:            req.ClientApp,
		SidecarGroup:       res.PolicyGroup,
		SidecarPick:        res.Model,
		RankedFallback:     res.RankedFallback,
		CandidateRosterIDs: candidateRosterIDs,
	}
}
