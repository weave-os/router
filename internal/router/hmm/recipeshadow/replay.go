package recipeshadow

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/armid"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/rosterdata"
)

var ErrNoClassifierFacts = errors.New("served decision carries no classifier facts to replay")

type ProfileRoster struct {
	Roster       *rosterdata.Roster
	PolicySHA256 string
}

type ProfileRosters interface {
	ProfileRoster(ctx context.Context, profileKey string) (ProfileRoster, error)
}

type Replayer struct {
	rosters  ProfileRosters
	resolver *policy.Resolver
}

func NewReplayer(rosters ProfileRosters, availableProviders map[string]struct{}) *Replayer {
	return &Replayer{
		rosters:  rosters,
		resolver: policy.NewResolver(catalog.HMMRoutingTargetSet(availableProviders), availableProviders, armid.ForModel, policy.ManagedProviderPolicy()),
	}
}

func (r *Replayer) ServedFacts(served router.Decision) (router.RecipeShadowServed, bool) {
	if !hmm.IsDecision(served) || served.Metadata == nil {
		return router.RecipeShadowServed{}, false
	}
	facts := hmm.ClassifierTelemetryOf(served.Metadata)
	if facts.PredictedLabel == "" || len(facts.ClassOrder) == 0 {
		return router.RecipeShadowServed{}, false
	}
	return router.RecipeShadowServed{
		RosterArmID:    served.Metadata.SelectedRosterArmID,
		PolicyGroup:    facts.PolicyGroup,
		PredictedLabel: facts.PredictedLabel,
	}, true
}

func (r *Replayer) Replay(ctx context.Context, profileKey string, served router.Decision, req router.Request) (router.RecipeShadowPick, error) {
	if _, ok := r.ServedFacts(served); !ok {
		return router.RecipeShadowPick{}, ErrNoClassifierFacts
	}
	facts := hmm.ClassifierTelemetryOf(served.Metadata)
	profile, err := r.rosters.ProfileRoster(ctx, profileKey)
	if err != nil {
		return router.RecipeShadowPick{}, err
	}
	resolved := r.resolver.Resolve(req)
	if len(resolved.Candidates) == 0 {
		return router.RecipeShadowPick{}, fmt.Errorf("recipe shadow: %w", policy.ErrNoEligibleArm)
	}
	input := policy.SelectionInput{
		Strategy:           router.Strategy(served.Metadata.Strategy),
		ExecutionMode:      policy.ExecutionModeShadow,
		RouteID:            served.Metadata.RouteID,
		Harness:            policy.SelectionHarnessForClientApp(req.ClientApp),
		PredictedLabel:     facts.PredictedLabel,
		ClassOrder:         append([]string(nil), facts.ClassOrder...),
		ClassProbabilities: cloneProbabilities(facts.Probabilities),
		PreferredModels:    append([]string(nil), req.PreferredModels...),
	}
	for _, candidate := range resolved.Candidates {
		input.CandidateRosterIDs = append(input.CandidateRosterIDs, candidate.RosterID)
	}
	if req.RoutingKnobs != nil && req.RoutingKnobs.QualityBias != nil {
		bias := *req.RoutingKnobs.QualityBias
		input.QualityBias = &bias
	}
	pick, err := selection.Selector(profile.Roster)(ctx, input)
	if err != nil {
		return router.RecipeShadowPick{}, err
	}
	binding, ok := resolved.BindingForSelection("", pick.Arm)
	if !ok {
		return router.RecipeShadowPick{}, fmt.Errorf("recipe shadow picked %q, which no candidate serves: %w", pick.Arm, policy.ErrNoEligibleArm)
	}
	return router.RecipeShadowPick{
		Model:          binding.CatalogID,
		Provider:       binding.Provider,
		RosterArmID:    pick.Arm,
		PolicyGroup:    pick.Group,
		PolicySHA256:   profile.PolicySHA256,
		PredictedLabel: facts.PredictedLabel,
	}, nil
}

func cloneProbabilities(probabilities map[string]float64) map[string]float64 {
	cloned := make(map[string]float64, len(probabilities))
	for label, probability := range probabilities {
		cloned[label] = probability
	}
	return cloned
}

var _ router.RecipeShadowReplayer = (*Replayer)(nil)
