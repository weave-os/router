package floor

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
)

func (t *Turn) Floor() catalog.Tier {
	tier := catalog.TierUnknown
	for _, verdict := range t.verdicts() {
		tier = max(tier, hmm.EscalationTier(verdict.Floor))
	}
	return tier
}

func (t *Turn) Escalate() bool {
	for _, verdict := range t.verdicts() {
		if verdict.Escalate {
			return true
		}
	}
	return false
}

func (t *Turn) verdicts() []*escalation.Constraint {
	if t == nil {
		return nil
	}
	verdicts := make([]*escalation.Constraint, 0, 2)
	if t.xgb != nil {
		if verdict := t.xgb.verdict(); verdict != nil {
			verdicts = append(verdicts, verdict)
		}
	}
	if t.llm != nil {
		if verdict := t.llm.verdict(); verdict != nil {
			verdicts = append(verdicts, verdict)
		}
	}
	return verdicts
}

func (t *Turn) anyActive() bool {
	return t != nil && ((t.xgb != nil && t.xgb.active) || (t.llm != nil && t.llm.active))
}

func AtLeast(candidates []string, tier catalog.Tier) []string {
	if tier == catalog.TierUnknown {
		return candidates
	}
	eligible := make([]string, 0, len(candidates))
	for _, id := range candidates {
		if catalog.TierFor(id) >= tier {
			eligible = append(eligible, id)
		}
	}
	return eligible
}

func logShadowTierFloor(ctx context.Context, t *Turn, decision router.Decision) {
	if t.anyActive() {
		return
	}
	tier, escalate := t.Floor(), t.Escalate()
	if tier == catalog.TierUnknown && !escalate {
		return
	}
	var candidates []string
	if decision.Metadata != nil {
		candidates = decision.Metadata.CandidateModels
	}
	servedTier := catalog.TierFor(decision.Model)
	observability.FromContext(ctx).Info("Escalation tier floor shadow",
		"floor_tier", tier.String(),
		"escalate", escalate,
		"served_model", decision.Model,
		"served_tier", servedTier.String(),
		"served_meets_floor", servedTier >= tier,
		"candidates", len(candidates),
		"candidates_at_floor", len(AtLeast(candidates, tier)),
	)
}
