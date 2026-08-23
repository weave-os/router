package proxy

import (
	"context"

	"workweave/router/internal/router/hmm"
	"workweave/router/internal/router/policy"
)

// NewStruggleRoster wraps an HMM roster source for proxy.StruggleEscalationRoster.
func NewStruggleRoster(source policy.RosterSource) *struggleRoster {
	return &struggleRoster{source: source}
}

type struggleRoster struct {
	source policy.RosterSource
}

// SidewaysTarget returns the next-ranked arm in the same cluster that is not
// currentModel and passes check. check(model) should validate the candidate is
// dispatchable (available, not excluded, image-capable, binding exists).
func (r *struggleRoster) SidewaysTarget(
	ctx context.Context,
	policyGroup, currentModel string,
	exclude map[string]struct{},
	check func(model string) bool,
) (string, error) {
	snapshot, err := r.source.ClusterRoster(ctx)
	if err != nil {
		return "", err
	}
	arms := snapshot.Clusters[policyGroup]
	for _, armID := range arms {
		model := hmm.CatalogIDForRoster(armID)
		if model == "" || model == currentModel {
			continue
		}
		if exclude != nil {
			if _, ok := exclude[model]; ok {
				continue
			}
		}
		if !check(model) {
			continue
		}
		return model, nil
	}
	return "", nil
}
