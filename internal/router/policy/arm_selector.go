package policy

import (
	"context"
	"errors"

	"weave-os/router/internal/router"
)

// ErrNoEligibleArm is returned when deterministic selection exhausts every
// eligible arm reported by the policy sidecar.
var ErrNoEligibleArm = errors.New("no eligible arm in any ranked group")

// SelectionInput is the content-free classification the router selects an arm from.
type SelectionInput struct {
	Strategy                         router.Strategy
	ExecutionMode                    string
	RouteID                          string
	Harness                          string
	PredictedLabel                   string
	ClassOrder                       []string
	ClassProbabilities               map[string]float64
	ForcedGroup                      string
	CandidateRosterIDs               []string
	QualityBias                      *float64
	PreferredModels                  []string
	SubscriptionStatePreferredModels []string
	SubsidizedModelCostFactor        map[string]float64
	// RosterSHA256 pins selection to one boot-loaded roster; empty uses the
	// default. Unknown digests fail with router.ErrPolicyPinUnavailable.
	RosterSHA256 string
}

// SelectionPick is the router's selected arm.
type SelectionPick struct {
	Group            string
	Arm              string
	ArmScoresByGroup map[string]map[string]float32
	RankedFallback   []PreviewGroup
	Trace            SelectionTrace
	// RosterSHA256 identifies the roster that produced the pick; empty when the
	// selector has no roster identity.
	RosterSHA256 string
}

// SelectionTrace records the Go-owned inputs and result used for diagnostics.
type SelectionTrace = router.SelectionTrace

// ArmSelector picks the served arm from a sidecar classification. An error
// fails the turn: with a classifier-only sidecar there is no arm to fall back to.
type ArmSelector func(ctx context.Context, input SelectionInput) (SelectionPick, error)

// selectionInputFor snapshots the sidecar's classification for the arm selector.
func selectionInputFor(strategy router.Strategy, executionMode string, req router.Request, res Result, resolved ResolvedCandidates) SelectionInput {
	candidateRosterIDs := make([]string, 0, len(resolved.Candidates))
	for _, candidate := range resolved.Candidates {
		candidateRosterIDs = append(candidateRosterIDs, candidate.RosterID)
	}
	input := SelectionInput{
		Strategy:                         strategy,
		ExecutionMode:                    executionMode,
		RouteID:                          res.RouteID,
		Harness:                          req.ClientApp,
		PredictedLabel:                   res.PredictedLabel,
		ClassOrder:                       append([]string(nil), res.ClassOrder...),
		ClassProbabilities:               cloneProbabilities(res.ClassProbabilities),
		CandidateRosterIDs:               candidateRosterIDs,
		PreferredModels:                  append([]string(nil), req.PreferredModels...),
		SubscriptionStatePreferredModels: append([]string(nil), req.SubscriptionStatePreferredModels...),
		SubsidizedModelCostFactor:        cloneModelFactors(req.SubsidizedModelCostFactor),
	}
	if req.RoutingKnobs != nil && req.RoutingKnobs.QualityBias != nil {
		qualityBias := *req.RoutingKnobs.QualityBias
		input.QualityBias = &qualityBias
	}
	if req.ForceCluster != "" {
		if _, hasOverride := req.ClusterArmOverrides[req.ForceCluster]; !hasOverride {
			input.ForcedGroup = req.ForceCluster
		}
	}
	return input
}

func cloneModelFactors(factors map[string]float64) map[string]float64 {
	if len(factors) == 0 {
		return nil
	}
	cloned := make(map[string]float64, len(factors))
	for model, factor := range factors {
		cloned[model] = factor
	}
	return cloned
}

func cloneProbabilities(probabilities map[string]float64) map[string]float64 {
	cloned := make(map[string]float64, len(probabilities))
	for label, probability := range probabilities {
		cloned[label] = probability
	}
	return cloned
}
