package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
)

// struggleClusterLadder orders clusters cheapest-to-strongest. fast/explore/low
// share a rung because they carry identical arms and cost_ref in the roster JSON.
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

// EscalationTarget returns the top-ranked arm from the cheapest cluster above
// policyGroup, or the next-ranked arm within policyGroup when nothing above can
// serve. The returned cluster identifies where the target came from.
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
