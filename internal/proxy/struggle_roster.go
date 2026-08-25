package proxy

import (
	"context"
	"strings"

	"workweave/router/internal/router/hmm"
	"workweave/router/internal/router/policy"
)

// struggleClusterLadder orders the complexity clusters from cheapest to
// strongest, with each rung listing the labels of every roster vocabulary that
// sits at that cost point: the five-class roster's "fast"/"explore" share the
// bottom rung (identical arms and cost reference), and the four-class roster's
// "low"/"medium" are the same rungs as "fast"/"balanced".
var struggleClusterLadder = [][]string{
	{"fast", "explore", "low"},
	{"balanced", "medium"},
	{"high"},
	{"maximum"},
}

// clustersAbove returns every cluster on a higher rung than group, cheapest
// first. An unknown group has nothing above it.
func clustersAbove(group string) []string {
	group = strings.ToLower(strings.TrimSpace(group))
	rung := -1
	for i, peers := range struggleClusterLadder {
		for _, peer := range peers {
			if peer == group {
				rung = i
			}
		}
	}
	if rung < 0 {
		return nil
	}
	var above []string
	for _, peers := range struggleClusterLadder[rung+1:] {
		above = append(above, peers...)
	}
	return above
}

// NewStruggleRoster wraps an HMM roster source for proxy.StruggleEscalationRoster.
func NewStruggleRoster(source policy.RosterSource) *struggleRoster {
	return &struggleRoster{source: source}
}

type struggleRoster struct {
	source policy.RosterSource
}

// EscalationTarget returns the top-ranked dispatchable arm from the cheapest
// cluster above policyGroup, falling back to a sideways move (the next-ranked
// arm in policyGroup itself) when no higher cluster can serve the session. The
// returned cluster is the one the target came from. check(model) should
// validate the candidate is dispatchable (available, not excluded,
// image-capable, binding exists).
func (r *struggleRoster) EscalationTarget(
	ctx context.Context,
	policyGroup, currentModel string,
	exclude map[string]struct{},
	check func(model string) bool,
) (target, cluster string, err error) {
	snapshot, err := r.source.ClusterRoster(ctx)
	if err != nil {
		return "", "", err
	}
	for _, group := range append(clustersAbove(policyGroup), policyGroup) {
		if model := firstDispatchableArm(snapshot.Clusters[group], currentModel, exclude, check); model != "" {
			return model, group, nil
		}
	}
	return "", "", nil
}

func firstDispatchableArm(
	arms []string,
	currentModel string,
	exclude map[string]struct{},
	check func(model string) bool,
) string {
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
		return model
	}
	return ""
}
