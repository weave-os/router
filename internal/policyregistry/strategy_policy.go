package policyregistry

import (
	"context"
	"fmt"
	"slices"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/selectionpolicy"
)

type StrategyPolicy struct {
	Reference PolicyObject
	Policy    *selectionpolicy.Policy
}

func (s *Snapshot) policyForStrategy(strategy router.Strategy) (*selectionpolicy.Policy, PolicyObject) {
	if strategyPolicy, found := s.StrategyPolicies[strategy]; found {
		return strategyPolicy.Policy, strategyPolicy.Reference
	}
	return s.Policy, s.Release.Policy
}

func loadStrategyPolicies(ctx context.Context, store ServingStore, composition CandidateComposition) (map[router.Strategy]StrategyPolicy, error) {
	if len(composition.StrategyPolicies) == 0 {
		return nil, nil
	}
	loaded := make(map[router.Strategy]StrategyPolicy, len(composition.StrategyPolicies))
	for strategy, reference := range composition.StrategyPolicies {
		admitted, err := store.ReadPolicy(ctx, ObjectRef{URI: reference.URI, SHA256: reference.SHA256, Generation: reference.Generation})
		if err != nil {
			return nil, fmt.Errorf("read %s strategy policy %s: %w", strategy, reference.SHA256, err)
		}
		roster := admitted.Roster()
		if roster.SchemaVersion != composition.Requirements.PolicySchema || !slices.Equal(roster.ClassOrder, composition.Classifier.Identity.ClassOrder) {
			return nil, fmt.Errorf("%s strategy policy does not match classifier schema and taxonomy", strategy)
		}
		loaded[strategy] = StrategyPolicy{Reference: reference, Policy: admitted}
	}
	return loaded, nil
}
