package routingpolicy_test

import (
	"testing"
	"time"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"

	"github.com/stretchr/testify/assert"
)

// A synthetic long agentic thread: the switch decision prices the move by
// the destination model's own cache state, so the same pick is taken or
// held depending on how far the thread has grown since that model last
// served it.
func TestDecideSwitchPricesTheDestinationCache(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	const prompt = 400_000
	cases := []struct {
		name   string
		input  routingpolicy.SwitchInput
		serve  string
		reason routingpolicy.SwitchReason
	}{
		{
			name: "downgrade back to a destination still inside the cache lookback",
			input: routingpolicy.SwitchInput{
				Current: switchTestHigh, Fresh: switchTestMid, EstimatedInputTokens: prompt, PromptTokens: prompt,
				Now: now, MessageCount: 300,
				ModelCaches: sessionpin.ModelCaches{
					switchTestHigh: {ServedAt: now.Add(-20 * time.Second), PromptTokens: prompt - 2_000, MessageCount: 298},
					switchTestMid:  {ServedAt: now.Add(-time.Minute), PromptTokens: prompt - 4_000, MessageCount: 296},
				},
			},
			serve: switchTestMid, reason: routingpolicy.SwitchReasonDowngrade,
		},
		{
			name: "downgrade holds when the destination fell out of the cache lookback",
			input: routingpolicy.SwitchInput{
				Current: switchTestHigh, Fresh: switchTestMid, EstimatedInputTokens: prompt, PromptTokens: prompt,
				Now: now, MessageCount: 300,
				ModelCaches: sessionpin.ModelCaches{
					switchTestHigh: {ServedAt: now.Add(-20 * time.Second), PromptTokens: prompt - 2_000, MessageCount: 298},
					switchTestMid:  {ServedAt: now.Add(-2 * time.Minute), PromptTokens: prompt - 60_000, MessageCount: 270},
				},
			},
			serve: switchTestHigh, reason: routingpolicy.SwitchReasonDowngradePending,
		},
		{
			name: "downgrade moves when the current cache expired too",
			input: routingpolicy.SwitchInput{
				Current: switchTestHigh, Fresh: switchTestMid, EstimatedInputTokens: prompt, PromptTokens: prompt,
				Now: now, MessageCount: 300, CacheCold: true,
				ModelCaches: sessionpin.ModelCaches{
					switchTestHigh: {ServedAt: now.Add(-2 * time.Hour), PromptTokens: prompt - 2_000, MessageCount: 298},
				},
			},
			serve: switchTestMid, reason: routingpolicy.SwitchReasonDowngrade,
		},
		{
			name: "upgrade on a small prompt is served at once",
			input: routingpolicy.SwitchInput{
				Current: switchTestLow, Fresh: switchTestHigh, EstimatedInputTokens: 30_000, PromptTokens: 30_000,
				Now: now, MessageCount: 12,
				ModelCaches: sessionpin.ModelCaches{
					switchTestLow: {ServedAt: now.Add(-10 * time.Second), PromptTokens: 29_000, MessageCount: 10},
				},
			},
			serve: switchTestHigh, reason: routingpolicy.SwitchReasonUpgrade,
		},
		{
			name: "upgrade into a cold destination holds when the rewrite outweighs the horizon",
			input: routingpolicy.SwitchInput{
				Current: switchTestMid, Fresh: switchTestHigh, EstimatedInputTokens: 900_000, PromptTokens: 900_000,
				Now: now, MessageCount: 600,
				ModelCaches: sessionpin.ModelCaches{
					switchTestMid: {ServedAt: now.Add(-10 * time.Second), PromptTokens: 898_000, MessageCount: 598},
				},
			},
			serve: switchTestMid, reason: routingpolicy.SwitchReasonUpgradePending,
		},
		{
			name: "upgrade back to a warm destination is served at once",
			input: routingpolicy.SwitchInput{
				Current: switchTestMid, Fresh: switchTestHigh, EstimatedInputTokens: 900_000, PromptTokens: 900_000,
				Now: now, MessageCount: 600,
				ModelCaches: sessionpin.ModelCaches{
					switchTestMid:  {ServedAt: now.Add(-10 * time.Second), PromptTokens: 898_000, MessageCount: 598},
					switchTestHigh: {ServedAt: now.Add(-40 * time.Second), PromptTokens: 896_000, MessageCount: 596},
				},
			},
			serve: switchTestHigh, reason: routingpolicy.SwitchReasonUpgrade,
		},
		{
			name: "upgrade to a prefix-cached destination is not limited by the block lookback",
			input: routingpolicy.SwitchInput{
				Current: switchTestLow, Fresh: switchTestPrefixCached, EstimatedInputTokens: 200_000, PromptTokens: 200_000,
				Now: now, MessageCount: 330,
				ModelCaches: sessionpin.ModelCaches{
					switchTestLow:          {ServedAt: now.Add(-10 * time.Second), PromptTokens: 199_000, MessageCount: 328},
					switchTestPrefixCached: {ServedAt: now.Add(-10 * time.Minute), PromptTokens: 190_000, MessageCount: 300},
				},
			},
			serve: switchTestPrefixCached, reason: routingpolicy.SwitchReasonUpgrade,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routingpolicy.DecideSwitch(tc.input)
			assert.Equal(t, tc.serve, got.Serve)
			assert.Equal(t, tc.reason, got.Reason)
		})
	}
}
