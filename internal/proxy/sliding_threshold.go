package proxy

import (
	"fmt"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
)

type slidingUpgradeExplanation struct {
	PinnedGroup         escalation.Group
	ClassifiedGroup     escalation.Group
	Probability         float64
	TargetGroup         escalation.Group
	Thresholds          escalation.SlidingUpgradeThresholds
	ConfidenceThreshold float64
}

func slidingUpgradeReason(res turnLoopResult) string {
	upgrade := res.SlidingUpgrade
	if upgrade == nil {
		return ""
	}
	prediction := fmt.Sprintf("%s difficulty predicted (%.1f%% confidence)", upgrade.ClassifiedGroup, upgrade.Probability*100)
	switch pinTier(res.PinTier) {
	case pinTierAuthoritativeSlidingThresholdUpgrade:
		if upgrade.TargetGroup != "" && upgrade.TargetGroup != upgrade.ClassifiedGroup {
			return fmt.Sprintf("%s; gradual upgrade from %s to %s (%.0f%% needed for %s)", prediction, upgrade.PinnedGroup, upgrade.TargetGroup, upgrade.Thresholds.FullProbability*100, upgrade.ClassifiedGroup)
		}
		return fmt.Sprintf("%s; upgraded from %s to %s (sliding threshold met)", prediction, upgrade.PinnedGroup, upgrade.ClassifiedGroup)
	case pinTierAuthoritativeUpgradeConfidenceLow:
		return fmt.Sprintf("%s; kept %s model (%.0f%% needed for sliding upgrade; %.0f%% confidence gate)", prediction, upgrade.PinnedGroup, upgrade.Thresholds.StepProbability*100, upgrade.ConfidenceThreshold*100)
	case pinTierAuthoritativeUpgradeEvidence:
		return fmt.Sprintf("%s; kept %s model pending stronger upgrade evidence", prediction, upgrade.PinnedGroup)
	case pinTierAuthoritativeDowngradeConfidenceLow:
		return fmt.Sprintf("%s; kept %s model (below the %.0f%% model-change confidence gate)", prediction, upgrade.PinnedGroup, upgrade.ConfidenceThreshold*100)
	case pinTierAuthoritativeDowngradeHysteresis:
		return fmt.Sprintf("%s; kept %s model pending another cheaper-model confirmation", prediction, upgrade.PinnedGroup)
	}
	return ""
}

func slidingUpgradeLogFields(res turnLoopResult) []any {
	upgrade := res.SlidingUpgrade
	if upgrade == nil {
		return nil
	}
	selectedGroup := ""
	if res.StickyHit {
		selectedGroup = res.PinPolicyGroup
	}
	proposedGroup := ""
	if res.Fresh.Metadata != nil {
		proposedGroup = res.Fresh.Metadata.PolicyGroup
	}
	if res.Decision.Metadata != nil {
		selectedGroup = res.Decision.Metadata.PolicyGroup
	}
	return []any{
		"routing_adjustment", res.PinTier,
		"classified_group", upgrade.ClassifiedGroup,
		"classification_probability", upgrade.Probability,
		"routing_previous_group", upgrade.PinnedGroup,
		"sliding_target_group", upgrade.TargetGroup,
		"sliding_step_threshold", upgrade.Thresholds.StepProbability,
		"sliding_full_threshold", upgrade.Thresholds.FullProbability,
		"upgrade_confidence_threshold", upgrade.ConfidenceThreshold,
		"routing_proposed_model", res.Fresh.Model,
		"routing_proposed_group", proposedGroup,
		"routing_selected_model", res.Decision.Model,
		"routing_selected_group", selectedGroup,
		"routing_explanation", slidingUpgradeReason(res),
	}
}

// slidingThresholdServes reports whether the sliding threshold moves the session
// to the fresh decision's group. When it does not, the existing upgrade gates
// decide the turn unchanged.
func slidingThresholdServes(req router.Request, pin sessionpin.Pin, fresh router.Decision) (served bool, probability float64) {
	if req.SlidingThresholdPinGroup == "" || pin.Model == "" || pin.Model == fresh.Model || fresh.Metadata == nil {
		return false, 0
	}
	label := fresh.Metadata.ClassifierPredictedLabel
	probability, scored := fresh.Metadata.ClassifierProbabilities[label]
	if !scored {
		return false, 0
	}
	target, covered := escalation.SlidingThresholdTarget(req.SlidingThresholdPinGroup, escalation.Group(label), probability)
	if !covered || target == "" {
		return false, probability
	}
	return escalation.Group(fresh.Metadata.PolicyGroup) == target, probability
}
