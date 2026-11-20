package proxy

import (
	"context"
	"slices"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"

	"github.com/google/uuid"
)

type authoritativeUpgradeDecision struct {
	Mode             flags.AuthoritativeUpgradePolicy
	Evidence         hmm.UpgradeEvidence
	Verdict          hmm.UpgradeVerdict
	PinModel         string
	PinProvider      string
	PinTier          catalog.Tier
	RequestedTier    catalog.Tier
	PinAgeSec        *int64
	MarginThreshold  *float64
	StalePinAfterSec *int64
	FreshScore       *float64
	Applied          bool
}

func (s *Service) resolveUpgradePolicyMode(ctx context.Context) flags.AuthoritativeUpgradePolicy {
	return s.ResolveAuthoritativeUpgradePolicy(ctx)
}

func (s *Service) authoritativeUpgradeFor(ctx context.Context, req router.Request, pin sessionpin.Pin, fresh router.Decision, res turnLoopResult, inputTokens int) *authoritativeUpgradeDecision {
	if !hmm.Owns(res.Strategy) || !isHMMDecision(fresh) {
		return nil
	}
	mode := s.resolveUpgradePolicyMode(ctx)
	if mode == flags.AuthoritativeUpgradePolicyScore && !s.ResolveAuthoritativeUpgradeGate(ctx) {
		mode = flags.AuthoritativeUpgradePolicyOff
	}
	decision := &authoritativeUpgradeDecision{
		Mode:            mode,
		PinModel:        pin.Model,
		PinProvider:     pin.Provider,
		PinTier:         catalog.TierFor(pin.Model),
		RequestedTier:   res.RequestedTier,
		MarginThreshold: s.hmmConfig.UpgradeShadow.MarginThreshold,
	}
	decision.Evidence = hmm.UpgradeEvidence{
		HasPin:         pin.Model != "",
		SameModel:      pin.Model == fresh.Model,
		FreshEligible:  candidates.AutomaticPinEligible(sessionpin.Pin{Model: fresh.Model, Provider: fresh.Provider}, req),
		Demoted:        slices.Contains(res.SessionDemotedModels, fresh.Model),
		FreshGroup:     escalation.Group(decisionPolicyGroup(fresh)),
		PinGroup:       escalation.Group(hmm.PinStateOf(pin).PolicyGroup),
		RequestedGroup: escalation.Group(req.ForceCluster),
		PrefixBroken:   res.PrefixTrimmed || req.HistoryTruncated,
		RequiredVotes:  s.ResolveAuthoritativeUpgradeVotes(ctx),
	}
	if s.availableModels != nil {
		_, available := s.availableModels[fresh.Model]
		decision.Evidence.FreshEligible = decision.Evidence.FreshEligible && available
	}
	if fresh.Metadata != nil {
		decision.Evidence.Margin = hmm.ClassifierMargin(fresh)
		score := float64(fresh.Metadata.ChosenScore)
		decision.FreshScore = &score
	}
	if decision.Evidence.Margin != nil && decision.MarginThreshold != nil {
		qualified := *decision.Evidence.Margin >= *decision.MarginThreshold
		decision.Evidence.MarginQualified = &qualified
	}
	if decision.Evidence.HasPin {
		decision.Evidence.CacheCold = pinCacheCold(pin, decision.Evidence.PrefixBroken, s.clock.Now())
		_, pinPriced := hmmEffectiveInputUSDPer1M(pin.Model, inputTokens)
		_, freshPriced := hmmEffectiveInputUSDPer1M(fresh.Model, inputTokens)
		decision.Evidence.PricingKnown = pinPriced && freshPriced
		decision.Evidence.MoreExpensive = hmm.FreshIsMoreExpensive(pin.Model, fresh.Model, inputTokens)
		if !pin.FirstPinnedAt.IsZero() {
			age := pinAge(pin, s.clock.Now())
			decision.PinAgeSec = &age
			decision.Evidence.StalePin = s.hmmConfig.UpgradeShadow.StalePinAfter > 0 && s.clock.Now().Sub(pin.FirstPinnedAt) >= s.hmmConfig.UpgradeShadow.StalePinAfter
		}
	}
	if s.hmmConfig.UpgradeShadow.StalePinAfter > 0 {
		seconds := int64(s.hmmConfig.UpgradeShadow.StalePinAfter.Seconds())
		decision.StalePinAfterSec = &seconds
	}
	votes := 0
	if decision.Evidence.HasPin && decision.Evidence.MoreExpensive && !decision.Evidence.SameModel &&
		decision.Evidence.FreshEligible && !decision.Evidence.Demoted &&
		decision.Evidence.FreshGroup == decision.Evidence.PinGroup &&
		!decision.Evidence.PrefixBroken && !decision.Evidence.CacheCold && !decision.Evidence.StalePin {
		votes = hmm.PinStateOf(pin).UpgradeVotes + 1
	}
	decision.Evidence.VoteCount = &votes
	decision.Verdict = hmm.EvaluateAuthoritativeUpgrade(decision.Evidence)
	return decision
}

func logAuthoritativeUpgrade(ctx context.Context, res turnLoopResult) {
	decision := res.UpgradeShadow
	if decision == nil {
		return
	}
	var wouldDiverge *bool
	switch decision.Verdict.Outcome {
	case hmm.UpgradeAllow:
		diverges := res.Decision.ServedIdentity() != res.Fresh.ServedIdentity()
		wouldDiverge = &diverges
	case hmm.UpgradeHold:
		diverges := !res.StickyHit
		wouldDiverge = &diverges
	case hmm.UpgradeExistingPolicy, hmm.UpgradeNotApplicable, hmm.UpgradeExcluded:
	}
	observability.FromContext(ctx).Info("authoritative upgrade evidence", "installation_id", res.InstallationID.String(), "role", res.PinRole, "mode", decision.Mode, "applied", decision.Applied, "turn_type", res.TurnType, "fresh_model", res.Fresh.Model, "fresh_provider", res.Fresh.Provider, "fresh_score", decision.FreshScore, "pin_model", decision.PinModel, "pin_provider", decision.PinProvider, "fresh_group", decision.Evidence.FreshGroup, "pin_group", decision.Evidence.PinGroup, "margin", decision.Evidence.Margin, "margin_threshold", decision.MarginThreshold, "requested_tier", decision.RequestedTier.String(), "pin_tier", decision.PinTier.String(), "explicit_requested_group", decision.Evidence.RequestedGroup, "cache_cold", decision.Evidence.CacheCold, "prefix_broken", decision.Evidence.PrefixBroken, "pin_age_sec", decision.PinAgeSec, "stale_pin_after_sec", decision.StalePinAfterSec, "vote_count", decision.Evidence.VoteCount, "required_votes", decision.Evidence.RequiredVotes, "demoted", decision.Evidence.Demoted, "fresh_eligible", decision.Evidence.FreshEligible, "outcome", decision.Verdict.Outcome, "reason", decision.Verdict.Reason, "served_model", res.Decision.Model, "pin_tier_path", res.PinTier, "would_diverge", wouldDiverge)
}

func authoritativeGuardsFor(ctx context.Context, s *Service, slidingServed bool) hmm.AuthoritativeGuards {
	return hmm.AuthoritativeGuards{
		ConfidenceThreshold:   s.hmmConfig.UpgradeConfidenceThreshold,
		UpgradeGate:           s.ResolveAuthoritativeUpgradeGate(ctx) && s.resolveUpgradePolicyMode(ctx) != flags.AuthoritativeUpgradePolicyOff && !slidingServed,
		DowngradeGate:         s.ResolveAuthoritativeDowngradeGate(ctx),
		HysteresisTurns:       s.ResolveHMMDowngradeHysteresisTurns(ctx),
		ShadowHysteresisTurns: s.ResolveHMMDowngradeHysteresisShadowTurns(ctx),
	}
}

func holdAuthoritativePin(
	ctx context.Context,
	sessionPins routingpolicy.SessionPins,
	installationID uuid.UUID,
	res *turnLoopResult,
	pin sessionpin.Pin,
	fresh router.Decision,
	verdict hmm.AuthoritativeVerdict,
) bool {
	if !hmm.Owns(res.Strategy) {
		return false
	}
	log := observability.FromContext(ctx)
	fields := []any{
		"pin_model", pin.Model,
		"pin_provider", pin.Provider,
		"fresh_model", fresh.Model,
		"fresh_provider", fresh.Provider,
		"confidence", verdict.Confidence,
		"threshold", verdict.Threshold,
	}
	if verdict.Downgrade {
		fields = append(fields, "downgrade_votes", verdict.DowngradeVotes, "hysteresis_turns", verdict.HysteresisTurns)
	}
	if verdict.Hold == hmm.AuthoritativeServe {
		if verdict.Downgrade {
			res.DowngradeShadow = downgradeGuardShadow{
				Served:              true,
				Votes:               verdict.DowngradeVotes,
				HysteresisWouldHold: verdict.HysteresisWouldHold,
				ConfidenceWouldHold: verdict.ConfidenceWouldHold,
			}
			fields = append(fields, "shadow_hysteresis_turns", verdict.ShadowHysteresisTurns, "shadow_hysteresis_would_hold", verdict.HysteresisWouldHold, "shadow_confidence_would_hold", verdict.ConfidenceWouldHold)
			log.Info("turnloop served authoritative downgrade", fields...)
		}
		return false
	}
	decision := pinDecision(pin)
	res.Decision = decision
	res.StickyHit = true
	switch verdict.Hold {
	case hmm.AuthoritativeHoldUpgradeConfidenceLow:
		res.PinTier = string(pinTierAuthoritativeUpgradeConfidenceLow)
		log.Info("turnloop suppressed low-confidence authoritative upgrade; keeping session pin", fields...)
		sessionPins.Refresh(ctx, installationID, res.SessionKey, pin, res.PinRole, decision)
	case hmm.AuthoritativeHoldDowngradeConfidenceLow:
		res.PinTier = string(pinTierAuthoritativeDowngradeConfidenceLow)
		log.Info("turnloop suppressed low-confidence authoritative downgrade; keeping session pin", fields...)
		sessionPins.RefreshDowngradeVotes(ctx, installationID, res.SessionKey, pin, res.PinRole, decision, verdict.DowngradeVotes)
	default:
		res.PinTier = string(pinTierAuthoritativeDowngradeHysteresis)
		log.Info("turnloop held authoritative downgrade below the hysteresis threshold; keeping session pin", fields...)
		sessionPins.RefreshDowngradeVotes(ctx, installationID, res.SessionKey, pin, res.PinRole, decision, verdict.DowngradeVotes)
	}
	return true
}
