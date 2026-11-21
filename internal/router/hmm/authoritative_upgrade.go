package hmm

import (
	"math"

	"weave-os/router/internal/router/continuity"

	"weave-os/router/internal/router/escalation"
)

type UpgradeOutcome = continuity.UpgradeOutcome

const (
	UpgradeAllow          = continuity.UpgradeAllow
	UpgradeHold           = continuity.UpgradeHold
	UpgradeExistingPolicy = continuity.UpgradeExistingPolicy
	UpgradeNotApplicable  = continuity.UpgradeNotApplicable
	UpgradeExcluded       = continuity.UpgradeExcluded
)

type UpgradeReason = continuity.UpgradeReason

const (
	UpgradeColdPin              = continuity.UpgradeColdPin
	UpgradePrefixBroken         = continuity.UpgradePrefixBroken
	UpgradeExplicitQualityFloor = continuity.UpgradeExplicitQualityFloor
	UpgradeGroupEscalation      = continuity.UpgradeGroupEscalation
	UpgradeMarginQualified      = continuity.UpgradeMarginQualified
	UpgradeRepeatedVote         = continuity.UpgradeRepeatedVote
	UpgradeWarmPinHold          = continuity.UpgradeWarmPinHold
	UpgradeInsufficientMargin   = continuity.UpgradeInsufficientMargin
	UpgradeMissingGroup         = continuity.UpgradeMissingGroup
	UpgradeMissingMargin        = continuity.UpgradeMissingMargin
	UpgradeInvalidMargin        = continuity.UpgradeInvalidMargin
	UpgradeUncalibratedMargin   = continuity.UpgradeUncalibratedMargin
	UpgradeDemotedModel         = continuity.UpgradeDemotedModel
	UpgradeIneligibleModel      = continuity.UpgradeIneligibleModel
	UpgradeNoPin                = continuity.UpgradeNoPin
	UpgradeSameModel            = continuity.UpgradeSameModel
	UpgradeNotMoreExpensive     = continuity.UpgradeNotMoreExpensive
	UpgradeMissingPricing       = continuity.UpgradeMissingPricing
	UpgradeStalePin             = continuity.UpgradeStalePin
)

type UpgradeVerdict = continuity.UpgradeVerdict

type UpgradeEvidence = continuity.UpgradeEvidence

func EvaluateAuthoritativeUpgrade(evidence UpgradeEvidence) UpgradeVerdict {
	switch {
	case evidence.Demoted:
		return UpgradeVerdict{Outcome: UpgradeExcluded, Reason: UpgradeDemotedModel}
	case !evidence.FreshEligible:
		return UpgradeVerdict{Outcome: UpgradeExcluded, Reason: UpgradeIneligibleModel}
	case !evidence.HasPin:
		return UpgradeVerdict{Outcome: UpgradeNotApplicable, Reason: UpgradeNoPin}
	case evidence.SameModel:
		return UpgradeVerdict{Outcome: UpgradeNotApplicable, Reason: UpgradeSameModel}
	case !evidence.PricingKnown:
		return UpgradeVerdict{Outcome: UpgradeExistingPolicy, Reason: UpgradeMissingPricing}
	case !evidence.MoreExpensive:
		return UpgradeVerdict{Outcome: UpgradeNotApplicable, Reason: UpgradeNotMoreExpensive}
	case escalation.Rank(evidence.FreshGroup) < 0 || escalation.Rank(evidence.PinGroup) < 0:
		return UpgradeVerdict{Outcome: UpgradeExistingPolicy, Reason: UpgradeMissingGroup}
	case evidence.PrefixBroken:
		return UpgradeVerdict{Outcome: UpgradeAllow, Reason: UpgradePrefixBroken}
	case evidence.CacheCold:
		return UpgradeVerdict{Outcome: UpgradeAllow, Reason: UpgradeColdPin}
	case (evidence.RequestedGroup == escalation.High || evidence.RequestedGroup == escalation.Maximum) &&
		escalation.Rank(evidence.RequestedGroup) > escalation.Rank(evidence.PinGroup) &&
		escalation.Rank(evidence.FreshGroup) >= escalation.Rank(evidence.RequestedGroup):
		return UpgradeVerdict{Outcome: UpgradeAllow, Reason: UpgradeExplicitQualityFloor}
	case evidence.StalePin:
		return UpgradeVerdict{Outcome: UpgradeAllow, Reason: UpgradeStalePin}
	case evidence.Margin == nil:
		return UpgradeVerdict{Outcome: UpgradeExistingPolicy, Reason: UpgradeMissingMargin}
	case math.IsNaN(*evidence.Margin) || math.IsInf(*evidence.Margin, 0) || *evidence.Margin < 0 || *evidence.Margin > 1:
		return UpgradeVerdict{Outcome: UpgradeExistingPolicy, Reason: UpgradeInvalidMargin}
	case evidence.MarginQualified == nil:
		return UpgradeVerdict{Outcome: UpgradeExistingPolicy, Reason: UpgradeUncalibratedMargin}
	case *evidence.MarginQualified:
		if escalation.Rank(evidence.FreshGroup) > escalation.Rank(evidence.PinGroup) {
			return UpgradeVerdict{Outcome: UpgradeAllow, Reason: UpgradeGroupEscalation}
		}
		return UpgradeVerdict{Outcome: UpgradeAllow, Reason: UpgradeMarginQualified}
	case evidence.FreshGroup == evidence.PinGroup:
		if evidence.RequiredVotes > 0 && evidence.VoteCount != nil && *evidence.VoteCount >= evidence.RequiredVotes {
			return UpgradeVerdict{Outcome: UpgradeAllow, Reason: UpgradeRepeatedVote}
		}
		return UpgradeVerdict{Outcome: UpgradeHold, Reason: UpgradeWarmPinHold}
	default:
		return UpgradeVerdict{Outcome: UpgradeHold, Reason: UpgradeInsufficientMargin}
	}
}
