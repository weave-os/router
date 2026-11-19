package hmm

import (
	"math"

	"weave-os/router/internal/router/escalation"
)

type UpgradeOutcome string

const (
	UpgradeAllow          UpgradeOutcome = "allow"
	UpgradeHold           UpgradeOutcome = "hold"
	UpgradeExistingPolicy UpgradeOutcome = "existing_policy"
	UpgradeNotApplicable  UpgradeOutcome = "not_applicable"
	UpgradeExcluded       UpgradeOutcome = "excluded"
)

type UpgradeReason string

const (
	UpgradeColdPin              UpgradeReason = "cold_pin"
	UpgradePrefixBroken         UpgradeReason = "prefix_broken"
	UpgradeExplicitQualityFloor UpgradeReason = "explicit_quality_floor"
	UpgradeGroupEscalation      UpgradeReason = "group_escalation"
	UpgradeMarginQualified      UpgradeReason = "margin_qualified"
	UpgradeRepeatedVote         UpgradeReason = "repeated_vote"
	UpgradeWarmPinHold          UpgradeReason = "warm_pin_hold"
	UpgradeInsufficientMargin   UpgradeReason = "insufficient_margin"
	UpgradeMissingGroup         UpgradeReason = "missing_group"
	UpgradeMissingMargin        UpgradeReason = "missing_margin"
	UpgradeInvalidMargin        UpgradeReason = "invalid_margin"
	UpgradeUncalibratedMargin   UpgradeReason = "uncalibrated_margin"
	UpgradeDemotedModel         UpgradeReason = "demoted_model"
	UpgradeIneligibleModel      UpgradeReason = "ineligible_model"
	UpgradeNoPin                UpgradeReason = "no_pin"
	UpgradeSameModel            UpgradeReason = "same_model"
	UpgradeNotMoreExpensive     UpgradeReason = "not_more_expensive"
	UpgradeMissingPricing       UpgradeReason = "missing_pricing"
	UpgradeStalePin             UpgradeReason = "stale_pin"
)

type UpgradeVerdict struct {
	Outcome UpgradeOutcome
	Reason  UpgradeReason
}

type UpgradeEvidence struct {
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

func EvaluateAuthoritativeUpgrade(evidence UpgradeEvidence) UpgradeVerdict {
	switch {
	case evidence.Demoted:
		return UpgradeVerdict{UpgradeExcluded, UpgradeDemotedModel}
	case !evidence.FreshEligible:
		return UpgradeVerdict{UpgradeExcluded, UpgradeIneligibleModel}
	case !evidence.HasPin:
		return UpgradeVerdict{UpgradeNotApplicable, UpgradeNoPin}
	case evidence.SameModel:
		return UpgradeVerdict{UpgradeNotApplicable, UpgradeSameModel}
	case !evidence.PricingKnown:
		return UpgradeVerdict{UpgradeExistingPolicy, UpgradeMissingPricing}
	case !evidence.MoreExpensive:
		return UpgradeVerdict{UpgradeNotApplicable, UpgradeNotMoreExpensive}
	case escalation.Rank(evidence.FreshGroup) < 0 || escalation.Rank(evidence.PinGroup) < 0:
		return UpgradeVerdict{UpgradeExistingPolicy, UpgradeMissingGroup}
	case evidence.PrefixBroken:
		return UpgradeVerdict{UpgradeAllow, UpgradePrefixBroken}
	case evidence.CacheCold:
		return UpgradeVerdict{UpgradeAllow, UpgradeColdPin}
	case (evidence.RequestedGroup == escalation.High || evidence.RequestedGroup == escalation.Maximum) &&
		escalation.Rank(evidence.RequestedGroup) > escalation.Rank(evidence.PinGroup) &&
		escalation.Rank(evidence.FreshGroup) >= escalation.Rank(evidence.RequestedGroup):
		return UpgradeVerdict{UpgradeAllow, UpgradeExplicitQualityFloor}
	case evidence.StalePin:
		return UpgradeVerdict{UpgradeAllow, UpgradeStalePin}
	case evidence.Margin == nil:
		return UpgradeVerdict{UpgradeExistingPolicy, UpgradeMissingMargin}
	case math.IsNaN(*evidence.Margin) || math.IsInf(*evidence.Margin, 0) || *evidence.Margin < 0 || *evidence.Margin > 1:
		return UpgradeVerdict{UpgradeExistingPolicy, UpgradeInvalidMargin}
	case evidence.MarginQualified == nil:
		return UpgradeVerdict{UpgradeExistingPolicy, UpgradeUncalibratedMargin}
	case *evidence.MarginQualified:
		if escalation.Rank(evidence.FreshGroup) > escalation.Rank(evidence.PinGroup) {
			return UpgradeVerdict{UpgradeAllow, UpgradeGroupEscalation}
		}
		return UpgradeVerdict{UpgradeAllow, UpgradeMarginQualified}
	case evidence.FreshGroup == evidence.PinGroup:
		if evidence.RequiredVotes > 0 && evidence.VoteCount != nil && *evidence.VoteCount >= evidence.RequiredVotes {
			return UpgradeVerdict{UpgradeAllow, UpgradeRepeatedVote}
		}
		return UpgradeVerdict{UpgradeHold, UpgradeWarmPinHold}
	default:
		return UpgradeVerdict{UpgradeHold, UpgradeInsufficientMargin}
	}
}
