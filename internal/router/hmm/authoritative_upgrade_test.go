package hmm_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
)

func TestEvaluateAuthoritativeUpgrade(t *testing.T) {
	margin, zero, invalid := 0.2, 0.0, math.NaN()
	qualified, unqualified := true, false
	votes := 3
	base := hmm.UpgradeEvidence{
		HasPin: true, MoreExpensive: true, PricingKnown: true, FreshEligible: true,
		FreshGroup: escalation.High, PinGroup: escalation.High,
		Margin: &margin, MarginQualified: &unqualified,
	}
	for _, tc := range []struct {
		name   string
		change func(*hmm.UpgradeEvidence)
		want   hmm.UpgradeVerdict
	}{
		{"warm same group weak margin", func(e *hmm.UpgradeEvidence) {}, hmm.UpgradeVerdict{hmm.UpgradeHold, hmm.UpgradeWarmPinHold}},
		{"same group qualified", func(e *hmm.UpgradeEvidence) { e.MarginQualified = &qualified }, hmm.UpgradeVerdict{hmm.UpgradeAllow, hmm.UpgradeMarginQualified}},
		{"higher group qualified", func(e *hmm.UpgradeEvidence) { e.FreshGroup = escalation.Maximum; e.MarginQualified = &qualified }, hmm.UpgradeVerdict{hmm.UpgradeAllow, hmm.UpgradeGroupEscalation}},
		{"higher group weak", func(e *hmm.UpgradeEvidence) { e.FreshGroup = escalation.Maximum }, hmm.UpgradeVerdict{hmm.UpgradeHold, hmm.UpgradeInsufficientMargin}},
		{"cold pin", func(e *hmm.UpgradeEvidence) { e.CacheCold = true }, hmm.UpgradeVerdict{hmm.UpgradeAllow, hmm.UpgradeColdPin}},
		{"prefix broken", func(e *hmm.UpgradeEvidence) { e.PrefixBroken = true; e.CacheCold = true }, hmm.UpgradeVerdict{hmm.UpgradeAllow, hmm.UpgradePrefixBroken}},
		{"calibrated stale pin", func(e *hmm.UpgradeEvidence) { e.StalePin = true }, hmm.UpgradeVerdict{hmm.UpgradeAllow, hmm.UpgradeStalePin}},
		{"explicit high floor", func(e *hmm.UpgradeEvidence) { e.RequestedGroup = escalation.High; e.PinGroup = escalation.Medium }, hmm.UpgradeVerdict{hmm.UpgradeAllow, hmm.UpgradeExplicitQualityFloor}},
		{"explicit maximum floor", func(e *hmm.UpgradeEvidence) { e.RequestedGroup = escalation.Maximum; e.FreshGroup = escalation.Maximum }, hmm.UpgradeVerdict{hmm.UpgradeAllow, hmm.UpgradeExplicitQualityFloor}},
		{"fresh does not meet requested floor", func(e *hmm.UpgradeEvidence) { e.RequestedGroup = escalation.Maximum }, hmm.UpgradeVerdict{hmm.UpgradeHold, hmm.UpgradeWarmPinHold}},
		{"unknown requested group is not maximum", func(e *hmm.UpgradeEvidence) { e.RequestedGroup = escalation.Group("future") }, hmm.UpgradeVerdict{hmm.UpgradeHold, hmm.UpgradeWarmPinHold}},
		{"missing fresh group", func(e *hmm.UpgradeEvidence) { e.FreshGroup = "" }, hmm.UpgradeVerdict{hmm.UpgradeExistingPolicy, hmm.UpgradeMissingGroup}},
		{"unknown pin group even when cold", func(e *hmm.UpgradeEvidence) { e.PinGroup = escalation.Group("future"); e.CacheCold = true }, hmm.UpgradeVerdict{hmm.UpgradeExistingPolicy, hmm.UpgradeMissingGroup}},
		{"missing margin", func(e *hmm.UpgradeEvidence) { e.Margin = nil }, hmm.UpgradeVerdict{hmm.UpgradeExistingPolicy, hmm.UpgradeMissingMargin}},
		{"invalid margin", func(e *hmm.UpgradeEvidence) { e.Margin = &invalid }, hmm.UpgradeVerdict{hmm.UpgradeExistingPolicy, hmm.UpgradeInvalidMargin}},
		{"zero margin is evidence", func(e *hmm.UpgradeEvidence) { e.Margin = &zero }, hmm.UpgradeVerdict{hmm.UpgradeHold, hmm.UpgradeWarmPinHold}},
		{"uncalibrated margin", func(e *hmm.UpgradeEvidence) { e.MarginQualified = nil }, hmm.UpgradeVerdict{hmm.UpgradeExistingPolicy, hmm.UpgradeUncalibratedMargin}},
		{"repeated compatible votes", func(e *hmm.UpgradeEvidence) { e.VoteCount = &votes; e.RequiredVotes = 3 }, hmm.UpgradeVerdict{hmm.UpgradeAllow, hmm.UpgradeRepeatedVote}},
		{"votes below threshold", func(e *hmm.UpgradeEvidence) { e.VoteCount = &votes; e.RequiredVotes = 4 }, hmm.UpgradeVerdict{hmm.UpgradeHold, hmm.UpgradeWarmPinHold}},
		{"votes unavailable", func(e *hmm.UpgradeEvidence) { e.RequiredVotes = 3 }, hmm.UpgradeVerdict{hmm.UpgradeHold, hmm.UpgradeWarmPinHold}},
		{"votes cannot rescue missing evidence", func(e *hmm.UpgradeEvidence) { e.VoteCount = &votes; e.RequiredVotes = 3; e.Margin = nil }, hmm.UpgradeVerdict{hmm.UpgradeExistingPolicy, hmm.UpgradeMissingMargin}},
		{"demotion precedes cold floor votes", func(e *hmm.UpgradeEvidence) {
			e.Demoted = true
			e.CacheCold = true
			e.RequestedGroup = escalation.Maximum
			e.VoteCount = &votes
			e.RequiredVotes = 3
		}, hmm.UpgradeVerdict{hmm.UpgradeExcluded, hmm.UpgradeDemotedModel}},
		{"unavailable fresh arm", func(e *hmm.UpgradeEvidence) { e.FreshEligible = false }, hmm.UpgradeVerdict{hmm.UpgradeExcluded, hmm.UpgradeIneligibleModel}},
		{"no pin", func(e *hmm.UpgradeEvidence) { e.HasPin = false }, hmm.UpgradeVerdict{hmm.UpgradeNotApplicable, hmm.UpgradeNoPin}},
		{"same model", func(e *hmm.UpgradeEvidence) { e.SameModel = true }, hmm.UpgradeVerdict{hmm.UpgradeNotApplicable, hmm.UpgradeSameModel}},
		{"missing pricing", func(e *hmm.UpgradeEvidence) { e.PricingKnown = false }, hmm.UpgradeVerdict{hmm.UpgradeExistingPolicy, hmm.UpgradeMissingPricing}},
		{"cheaper proposal", func(e *hmm.UpgradeEvidence) { e.MoreExpensive = false }, hmm.UpgradeVerdict{hmm.UpgradeNotApplicable, hmm.UpgradeNotMoreExpensive}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := base
			tc.change(&evidence)
			assert.Equal(t, tc.want, hmm.EvaluateAuthoritativeUpgrade(evidence))
		})
	}
}
