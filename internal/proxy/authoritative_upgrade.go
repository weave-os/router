package proxy

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
)

// AuthoritativeUpgradeConfig holds evidence-policy calibration. Nil margin and
// zero age leave those rules uncalibrated; the evaluator then falls back to the
// existing score gate instead of inventing quality cutoffs.
type AuthoritativeUpgradeConfig struct {
	MarginThreshold *float64
	StalePinAfter   time.Duration
}

// Validate rejects invalid deployment calibration rather than silently substituting defaults.
func (c AuthoritativeUpgradeConfig) Validate() error {
	if c.MarginThreshold != nil && (math.IsNaN(*c.MarginThreshold) || math.IsInf(*c.MarginThreshold, 0) || *c.MarginThreshold < 0 || *c.MarginThreshold > 1) {
		return fmt.Errorf("upgrade evidence margin threshold must be finite and within [0, 1]")
	}
	if c.StalePinAfter < 0 {
		return fmt.Errorf("upgrade evidence stale-pin age must be nonnegative")
	}
	return nil
}

// WithAuthoritativeUpgradeConfig installs boot-validated evidence calibration.
func (s *Service) WithAuthoritativeUpgradeConfig(calibration AuthoritativeUpgradeConfig) *Service {
	s.authoritativeUpgradeConfig = calibration
	return s
}

type upgradeOutcome string

const (
	upgradeAllow          upgradeOutcome = "allow"
	upgradeHold           upgradeOutcome = "hold"
	upgradeExistingPolicy upgradeOutcome = "existing_policy"
	upgradeNotApplicable  upgradeOutcome = "not_applicable"
	upgradeExcluded       upgradeOutcome = "excluded"
)

type upgradeReason string

const (
	upgradeColdPin              upgradeReason = "cold_pin"
	upgradePrefixBroken         upgradeReason = "prefix_broken"
	upgradeExplicitQualityFloor upgradeReason = "explicit_quality_floor"
	upgradeGroupEscalation      upgradeReason = "group_escalation"
	upgradeMarginQualified      upgradeReason = "margin_qualified"
	upgradeRepeatedVote         upgradeReason = "repeated_vote"
	upgradeWarmPinHold          upgradeReason = "warm_pin_hold"
	upgradeInsufficientMargin   upgradeReason = "insufficient_margin"
	upgradeMissingGroup         upgradeReason = "missing_group"
	upgradeMissingMargin        upgradeReason = "missing_margin"
	upgradeInvalidMargin        upgradeReason = "invalid_margin"
	upgradeUncalibratedMargin   upgradeReason = "uncalibrated_margin"
	upgradeDemotedModel         upgradeReason = "demoted_model"
	upgradeIneligibleModel      upgradeReason = "ineligible_model"
	upgradeNoPin                upgradeReason = "no_pin"
	upgradeSameModel            upgradeReason = "same_model"
	upgradeNotMoreExpensive     upgradeReason = "not_more_expensive"
	upgradeMissingPricing       upgradeReason = "missing_pricing"
	upgradeStalePin             upgradeReason = "stale_pin"
)

type upgradeVerdict struct {
	Outcome upgradeOutcome
	Reason  upgradeReason
}

// Threshold comparisons happen at the boundary, so replay can evaluate several
// calibrations over identical evidence without reading clocks, stores, or flags.
type upgradeEvidence struct {
	HasPin          bool
	SameModel       bool
	MoreExpensive   bool
	PricingKnown    bool
	FreshEligible   bool
	Demoted         bool
	FreshGroup      escalation.Group
	PinGroup        escalation.Group
	RequestedGroup  escalation.Group
	CacheCold       bool
	PrefixBroken    bool
	Margin          *float64
	MarginQualified *bool
	StalePin        bool
	VoteCount       *int
	RequiredVotes   int
}

func evaluateAuthoritativeUpgrade(evidence upgradeEvidence) upgradeVerdict {
	switch {
	case evidence.Demoted:
		return upgradeVerdict{upgradeExcluded, upgradeDemotedModel}
	case !evidence.FreshEligible:
		return upgradeVerdict{upgradeExcluded, upgradeIneligibleModel}
	case !evidence.HasPin:
		return upgradeVerdict{upgradeNotApplicable, upgradeNoPin}
	case evidence.SameModel:
		return upgradeVerdict{upgradeNotApplicable, upgradeSameModel}
	case !evidence.PricingKnown:
		return upgradeVerdict{upgradeExistingPolicy, upgradeMissingPricing}
	case !evidence.MoreExpensive:
		return upgradeVerdict{upgradeNotApplicable, upgradeNotMoreExpensive}
	case escalation.Rank(evidence.FreshGroup) < 0 || escalation.Rank(evidence.PinGroup) < 0:
		return upgradeVerdict{upgradeExistingPolicy, upgradeMissingGroup}
	case evidence.PrefixBroken:
		return upgradeVerdict{upgradeAllow, upgradePrefixBroken}
	case evidence.CacheCold:
		return upgradeVerdict{upgradeAllow, upgradeColdPin}
	case (evidence.RequestedGroup == escalation.High || evidence.RequestedGroup == escalation.Maximum) &&
		escalation.Rank(evidence.RequestedGroup) > escalation.Rank(evidence.PinGroup) &&
		escalation.Rank(evidence.FreshGroup) >= escalation.Rank(evidence.RequestedGroup):
		return upgradeVerdict{upgradeAllow, upgradeExplicitQualityFloor}
	case evidence.StalePin:
		return upgradeVerdict{upgradeAllow, upgradeStalePin}
	case evidence.Margin == nil:
		return upgradeVerdict{upgradeExistingPolicy, upgradeMissingMargin}
	case math.IsNaN(*evidence.Margin) || math.IsInf(*evidence.Margin, 0) || *evidence.Margin < 0 || *evidence.Margin > 1:
		return upgradeVerdict{upgradeExistingPolicy, upgradeInvalidMargin}
	case evidence.MarginQualified == nil:
		return upgradeVerdict{upgradeExistingPolicy, upgradeUncalibratedMargin}
	case *evidence.MarginQualified:
		if escalation.Rank(evidence.FreshGroup) > escalation.Rank(evidence.PinGroup) {
			return upgradeVerdict{upgradeAllow, upgradeGroupEscalation}
		}
		return upgradeVerdict{upgradeAllow, upgradeMarginQualified}
	case evidence.FreshGroup == evidence.PinGroup:
		if evidence.RequiredVotes > 0 && evidence.VoteCount != nil && *evidence.VoteCount >= evidence.RequiredVotes {
			return upgradeVerdict{upgradeAllow, upgradeRepeatedVote}
		}
		return upgradeVerdict{upgradeHold, upgradeWarmPinHold}
	default:
		return upgradeVerdict{upgradeHold, upgradeInsufficientMargin}
	}
}

type authoritativeUpgradeDecision struct {
	Mode             flags.AuthoritativeUpgradePolicy
	Evidence         upgradeEvidence
	Verdict          upgradeVerdict
	PinModel         string
	PinProvider      string
	PinTier          catalog.Tier
	RequestedTier    catalog.Tier
	PinAgeSec        *int64
	MarginThreshold  *float64
	StalePinAfterSec *int64
	FreshScore       *float64
	HoldoutControl   bool
	Applied          bool
}

func (s *Service) resolveUpgradePolicyMode(ctx context.Context) flags.AuthoritativeUpgradePolicy {
	return s.ResolveAuthoritativeUpgradePolicy(ctx)
}

func (s *Service) evidenceUpgradeApplies(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte) bool {
	if s.resolveUpgradePolicyMode(ctx) != flags.AuthoritativeUpgradePolicyEvidence {
		return false
	}
	return !DeterministicHoldout(sessionKey, s.ResolveAuthoritativeUpgradeHoldoutPct(ctx))
}

func (s *Service) authoritativeUpgradeFor(ctx context.Context, req router.Request, pin sessionpin.Pin, fresh router.Decision, res turnLoopResult, inputTokens int) *authoritativeUpgradeDecision {
	if !isHMMDecision(fresh) {
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
		MarginThreshold: s.authoritativeUpgradeConfig.MarginThreshold,
		HoldoutControl:  mode == flags.AuthoritativeUpgradePolicyEvidence && DeterministicHoldout(res.SessionKey, s.ResolveAuthoritativeUpgradeHoldoutPct(ctx)),
	}
	decision.Evidence = upgradeEvidence{
		HasPin:         pin.Model != "",
		SameModel:      pin.Model == fresh.Model,
		FreshEligible:  automaticPinEligible(sessionpin.Pin{Model: fresh.Model, Provider: fresh.Provider}, req),
		Demoted:        slices.Contains(res.SessionDemotedModels, fresh.Model),
		FreshGroup:     escalation.Group(decisionPolicyGroup(fresh)),
		PinGroup:       escalation.Group(pin.PolicyGroup),
		RequestedGroup: escalation.Group(req.ForceCluster),
		PrefixBroken:   res.PrefixTrimmed || req.HistoryTruncated,
		RequiredVotes:  s.ResolveAuthoritativeUpgradeVotes(ctx),
	}
	if s.availableModels != nil {
		_, available := s.availableModels[fresh.Model]
		decision.Evidence.FreshEligible = decision.Evidence.FreshEligible && available
	}
	if fresh.Metadata != nil {
		decision.Evidence.Margin = fresh.Metadata.ClassifierMargin
		score := float64(fresh.Metadata.ChosenScore)
		decision.FreshScore = &score
	}
	if decision.Evidence.Margin != nil && decision.MarginThreshold != nil {
		qualified := *decision.Evidence.Margin >= *decision.MarginThreshold
		decision.Evidence.MarginQualified = &qualified
	}
	if decision.Evidence.HasPin {
		decision.Evidence.CacheCold = pinCacheCold(pin, decision.Evidence.PrefixBroken)
		_, pinPriced := hmmEffectiveInputUSDPer1M(pin.Model, inputTokens, req.SubsidizedModelCostFactor)
		_, freshPriced := hmmEffectiveInputUSDPer1M(fresh.Model, inputTokens, req.SubsidizedModelCostFactor)
		decision.Evidence.PricingKnown = pinPriced && freshPriced
		decision.Evidence.MoreExpensive = hmmFreshIsMoreExpensive(pin.Model, fresh.Model, inputTokens, req.SubsidizedModelCostFactor)
		if !pin.FirstPinnedAt.IsZero() {
			age := pinAge(pin)
			decision.PinAgeSec = &age
			decision.Evidence.StalePin = s.authoritativeUpgradeConfig.StalePinAfter > 0 && time.Since(pin.FirstPinnedAt) >= s.authoritativeUpgradeConfig.StalePinAfter
		}
	}
	if s.authoritativeUpgradeConfig.StalePinAfter > 0 {
		seconds := int64(s.authoritativeUpgradeConfig.StalePinAfter.Seconds())
		decision.StalePinAfterSec = &seconds
	}
	// Votes are consecutive only while every proposal remains an eligible,
	// expensive, same-group upgrade. A held proposal outside that shape must
	// clear the counter before a later same-group proposal can vote again.
	votes := 0
	if decision.Evidence.HasPin && decision.Evidence.MoreExpensive && !decision.Evidence.SameModel &&
		decision.Evidence.FreshEligible && !decision.Evidence.Demoted &&
		decision.Evidence.FreshGroup == decision.Evidence.PinGroup &&
		!decision.Evidence.PrefixBroken && !decision.Evidence.CacheCold && !decision.Evidence.StalePin {
		votes = pin.ConsecutiveUpgradeVotes + 1
	}
	decision.Evidence.VoteCount = &votes
	decision.Verdict = evaluateAuthoritativeUpgrade(decision.Evidence)
	return decision
}

func logAuthoritativeUpgrade(ctx context.Context, res turnLoopResult) {
	decision := res.UpgradeShadow
	if decision == nil {
		return
	}
	var wouldDiverge *bool
	switch decision.Verdict.Outcome {
	case upgradeAllow:
		diverges := res.Decision.ServedIdentity() != res.Fresh.ServedIdentity()
		wouldDiverge = &diverges
	case upgradeHold:
		diverges := !res.StickyHit
		wouldDiverge = &diverges
	}
	observability.FromContext(ctx).Info("authoritative upgrade evidence", "installation_id", res.InstallationID.String(), "role", res.PinRole, "mode", decision.Mode, "applied", decision.Applied, "holdout_control", decision.HoldoutControl, "turn_type", res.TurnType, "fresh_model", res.Fresh.Model, "fresh_provider", res.Fresh.Provider, "fresh_score", decision.FreshScore, "pin_model", decision.PinModel, "pin_provider", decision.PinProvider, "fresh_group", decision.Evidence.FreshGroup, "pin_group", decision.Evidence.PinGroup, "margin", decision.Evidence.Margin, "margin_threshold", decision.MarginThreshold, "requested_tier", decision.RequestedTier.String(), "pin_tier", decision.PinTier.String(), "explicit_requested_group", decision.Evidence.RequestedGroup, "cache_cold", decision.Evidence.CacheCold, "prefix_broken", decision.Evidence.PrefixBroken, "pin_age_sec", decision.PinAgeSec, "stale_pin_after_sec", decision.StalePinAfterSec, "vote_count", decision.Evidence.VoteCount, "required_votes", decision.Evidence.RequiredVotes, "demoted", decision.Evidence.Demoted, "fresh_eligible", decision.Evidence.FreshEligible, "outcome", decision.Verdict.Outcome, "reason", decision.Verdict.Reason, "served_model", res.Decision.Model, "pin_tier_path", res.PinTier, "would_diverge", wouldDiverge)
}
