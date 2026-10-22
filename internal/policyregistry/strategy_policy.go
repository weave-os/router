package policyregistry

import (
	"context"
	"fmt"
	"slices"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
)

// StrategyPolicy pairs one beta strategy's immutable policy identity and roster.
type StrategyPolicy struct {
	Reference PolicyObject
	Roster    *rosterdata.Roster
}

func (s *Snapshot) policyForStrategy(strategy router.Strategy) (*rosterdata.Roster, PolicyObject) {
	if strategyPolicy, found := s.StrategyPolicies[strategy]; found {
		return strategyPolicy.Roster, strategyPolicy.Reference
	}
	return s.Policy, s.Release.Policy
}

func loadStrategyPolicies(ctx context.Context, store ServingStore, composition CandidateComposition) (map[router.Strategy]StrategyPolicy, error) {
	if len(composition.StrategyPolicies) == 0 {
		return nil, nil
	}
	loaded := make(map[router.Strategy]StrategyPolicy, len(composition.StrategyPolicies))
	for strategy, reference := range composition.StrategyPolicies {
		roster, err := store.ReadServingPolicy(ctx, ObjectRef{URI: reference.URI, SHA256: reference.SHA256, Generation: reference.Generation})
		if err != nil {
			return nil, fmt.Errorf("read %s strategy policy %s: %w", strategy, reference.SHA256, err)
		}
		if roster.SchemaVersion != composition.Requirements.PolicySchema || !slices.Equal(roster.ClassOrder, composition.Classifier.Identity.ClassOrder) {
			return nil, fmt.Errorf("%s strategy policy does not match classifier schema and taxonomy", strategy)
		}
		if diagnostics := armid.ValidateRosterIDs(roster.AllArms()); len(diagnostics) != 0 {
			return nil, fmt.Errorf("%s strategy policy is incompatible with the router catalog: %v", strategy, diagnostics)
		}
		loaded[strategy] = StrategyPolicy{Reference: reference, Roster: roster}
	}
	return loaded, nil
}
