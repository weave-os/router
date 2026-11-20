package routingpolicy_test

import (
	"testing"

	"weave-os/router/internal/routingpolicy"

	"github.com/stretchr/testify/assert"
)

func TestDecideSwitchCompactsWhenTheRewriteIsLarge(t *testing.T) {
	const (
		largePrompt     = 600_000
		compactedPrompt = 34_000
	)
	cases := []struct {
		name      string
		input     routingpolicy.SwitchInput
		serve     string
		compact   bool
		reason    routingpolicy.SwitchReason
		compacted int
	}{
		{"upgrade into an unseen destination compacts instead of holding below the pick", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestHigh}, switchTestHigh, true, routingpolicy.SwitchReasonCompactThenSwitch, compactedPrompt},
		{"downgrade into an unseen destination compacts instead of holding", routingpolicy.SwitchInput{Current: switchTestHigh, Fresh: switchTestMid}, switchTestMid, true, routingpolicy.SwitchReasonCompactThenSwitch, compactedPrompt},
		{"without a compaction quote the upgrade still holds", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestHigh}, switchTestMid, false, routingpolicy.SwitchReasonUpgradePending, 0},
		{"downgrade to a model served last turn moves without compacting", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestLow, FreshTurnsSinceServed: 1}, switchTestLow, false, routingpolicy.SwitchReasonDowngrade, compactedPrompt},
		{"same model never compacts", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestMid}, switchTestMid, false, routingpolicy.SwitchReasonSameModel, compactedPrompt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.input.EstimatedInputTokens = largePrompt
			tc.input.PromptTokens = largePrompt
			tc.input.CompactedTokens = tc.compacted
			verdict := routingpolicy.DecideSwitch(tc.input)
			assert.Equal(t, tc.serve, verdict.Serve)
			assert.Equal(t, tc.reason, verdict.Reason)
			assert.Equal(t, tc.compact, verdict.Compact)
			assert.Equal(t, tc.serve != tc.input.Current, verdict.Switch)
		})
	}
}

func TestDecideSwitchDoesNotCompactASmallPrompt(t *testing.T) {
	verdict := routingpolicy.DecideSwitch(routingpolicy.SwitchInput{
		Current: switchTestLow, Fresh: switchTestHigh, EstimatedInputTokens: 50_000, CompactedTokens: 34_000,
	})
	assert.Equal(t, routingpolicy.SwitchVerdict{Serve: switchTestHigh, Switch: true, Reason: routingpolicy.SwitchReasonUpgrade}, verdict)
}
