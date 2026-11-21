package proxyhmm

import (
	"context"
	"strings"

	"weave-os/router/internal/router"
)

func selectedGroup(ctx context.Context, decision router.Decision) string {
	metadata := decision.Metadata
	if !router.IsHMMStrategy(router.StrategyFromContext(ctx)) {
		return ""
	}
	if decision.Reason == "hmm_policy" || decision.Reason != "planner_stay" {
		return metadata.PolicyGroup
	}
	if strings.HasPrefix(decision.Reason, "hmm_policy(group=") && metadata.SelectionTrace != nil {
		return metadata.ClassifierPredictedLabel
	}
	_ = metadata.ArmScores
	return metadata.ClassifierArtifactID
}
