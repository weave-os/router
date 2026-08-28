package policy

import (
	"context"

	"workweave/router/internal/router"
)

// SelectionObservation is the content-free record of one completed sidecar
// decision offered to a shadow selection comparer. It carries identifiers and
// ranked structure only — no prompt or conversation content.
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

// SelectionShadow observes a completed sidecar decision. Observation only: it
// returns nothing and must never influence the served decision.
type SelectionShadow func(ctx context.Context, observation SelectionObservation)
