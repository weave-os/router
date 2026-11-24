package proxy

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/routingpolicy"
)

func logAuthoritativeUpgrade(ctx context.Context, res routingpolicy.TurnRoute) {
	decision := res.UpgradeShadow
	if decision == nil {
		return
	}
	var wouldDiverge *bool
	switch decision.Verdict.Outcome {
	case continuity.UpgradeAllow:
		diverges := res.Decision.ServedIdentity() != res.Fresh.ServedIdentity()
		wouldDiverge = &diverges
	case continuity.UpgradeHold:
		diverges := !res.StickyHit
		wouldDiverge = &diverges
	case continuity.UpgradeExistingPolicy, continuity.UpgradeNotApplicable, continuity.UpgradeExcluded:
	}
	observability.FromContext(ctx).Info("authoritative upgrade evidence", "installation_id", res.InstallationID.String(), "role", res.PinRole, "mode", decision.Mode, "applied", decision.Applied, "turn_type", res.TurnType, "fresh_model", res.Fresh.Model, "fresh_provider", res.Fresh.Provider, "fresh_score", decision.FreshScore, "pin_model", decision.PinModel, "pin_provider", decision.PinProvider, "fresh_group", decision.Evidence.FreshGroup, "pin_group", decision.Evidence.PinGroup, "margin", decision.Evidence.Margin, "margin_threshold", decision.MarginThreshold, "requested_tier", decision.RequestedTier.String(), "pin_tier", decision.PinTier.String(), "explicit_requested_group", decision.Evidence.RequestedGroup, "cache_cold", decision.Evidence.CacheCold, "prefix_broken", decision.Evidence.PrefixBroken, "pin_age_sec", decision.PinAgeSec, "stale_pin_after_sec", decision.StalePinAfterSec, "vote_count", decision.Evidence.VoteCount, "required_votes", decision.Evidence.RequiredVotes, "demoted", decision.Evidence.Demoted, "fresh_eligible", decision.Evidence.FreshEligible, "outcome", decision.Verdict.Outcome, "reason", decision.Verdict.Reason, "served_model", res.Decision.Model, "pin_tier_path", res.PinTier, "would_diverge", wouldDiverge)
}
