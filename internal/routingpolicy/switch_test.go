package routingpolicy_test

import (
	"testing"

	"weave-os/router/internal/routingpolicy"

	"github.com/stretchr/testify/assert"
)

const (
	switchTestLow     = "claude-haiku-5-5"
	switchTestMid     = "claude-sonnet-5-5"
	switchTestHigh    = "claude-opus-5-5"
	switchTestUnknown = "synthetic-unpriced-model"
)

func TestDecideSwitch(t *testing.T) {
	cases := []struct {
		name   string
		input  routingpolicy.SwitchInput
		serve  string
		reason routingpolicy.SwitchReason
	}{
		{"first turn serves the pick", routingpolicy.SwitchInput{Fresh: switchTestHigh}, switchTestHigh, routingpolicy.SwitchReasonFirstTurn},
		{"same model stays", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestMid}, switchTestMid, routingpolicy.SwitchReasonSameModel},
		{"unpriced current model serves the pick", routingpolicy.SwitchInput{Current: switchTestUnknown, Fresh: switchTestHigh}, switchTestHigh, routingpolicy.SwitchReasonCurrentUnavailable},
		{"unpriced pick stays", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestUnknown}, switchTestMid, routingpolicy.SwitchReasonPricingMissing},
		{"cheaper pick is served at once on a warm cache", routingpolicy.SwitchInput{Current: switchTestHigh, Fresh: switchTestLow}, switchTestLow, routingpolicy.SwitchReasonDowngrade},
		{"pricier pick is served at once on a cold cache", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh, CacheCold: true}, switchTestHigh, routingpolicy.SwitchReasonUpgradeFree},
		{"pricier pick is served at once on a broken prefix", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh, PrefixBroken: true}, switchTestHigh, routingpolicy.SwitchReasonUpgradeFree},
		{"first pricier pick waits", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh}, switchTestLow, routingpolicy.SwitchReasonUpgradePending},
		{"first pricier pick after cheaper picks waits", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestHigh, PriorPicks: []string{switchTestLow, switchTestMid}}, switchTestMid, routingpolicy.SwitchReasonUpgradePending},
		{"second consecutive pricier pick is served", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh, PriorPicks: []string{switchTestLow, switchTestHigh}}, switchTestHigh, routingpolicy.SwitchReasonUpgradeVotes},
		{"alternating picks upgrade on two of the last three", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh, PriorPicks: []string{switchTestHigh, switchTestLow}}, switchTestHigh, routingpolicy.SwitchReasonUpgradeVotes},
		{"pricier picks may differ and still count", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh, PriorPicks: []string{switchTestMid}}, switchTestHigh, routingpolicy.SwitchReasonUpgradeVotes},
		{"picks older than the window do not count", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh, PriorPicks: []string{switchTestHigh, switchTestLow, switchTestLow}}, switchTestLow, routingpolicy.SwitchReasonUpgradePending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.input.EstimatedInputTokens = 10_000
			verdict := routingpolicy.DecideSwitch(tc.input)
			assert.Equal(t, tc.serve, verdict.Serve)
			assert.Equal(t, tc.reason, verdict.Reason)
			assert.Equal(t, tc.serve != tc.input.Current, verdict.Switch)
		})
	}
}

func TestDecideSwitchNeverHoldsBelowAlternatingPicks(t *testing.T) {
	current := switchTestLow
	var picks []string
	heldPricierPicks := 0
	for turn := range 20 {
		fresh := switchTestLow
		if turn%2 == 0 {
			fresh = switchTestHigh
		}
		verdict := routingpolicy.DecideSwitch(routingpolicy.SwitchInput{Current: current, Fresh: fresh, PriorPicks: picks, EstimatedInputTokens: 10_000})
		if verdict.Serve == switchTestLow && fresh == switchTestHigh {
			heldPricierPicks++
		}
		current = verdict.Serve
		picks = append(picks, fresh)
	}
	assert.Equal(t, 1, heldPricierPicks, "only the first pricier pick is held")
}

func TestDecideSwitchPricesTheRewriteOfALargeWarmPrompt(t *testing.T) {
	const largePrompt = 600_000
	cases := []struct {
		name   string
		input  routingpolicy.SwitchInput
		serve  string
		reason routingpolicy.SwitchReason
	}{
		{"side request keeps the current model", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestLow, SideRequest: true}, switchTestMid, routingpolicy.SwitchReasonSideRequest},
		{"warm downgrade whose rewrite exceeds the read savings holds", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestLow}, switchTestMid, routingpolicy.SwitchReasonDowngradePending},
		{"warm downgrade holds when only the estimate is small", routingpolicy.SwitchInput{Current: switchTestHigh, Fresh: switchTestMid, EstimatedInputTokens: 80_000}, switchTestHigh, routingpolicy.SwitchReasonDowngradePending},
		{"downgrade to a model served last turn moves", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestLow, FreshTurnsSinceServed: 1}, switchTestLow, routingpolicy.SwitchReasonDowngradeWarm},
		{"downgrade to a model whose cache aged out holds", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestLow, FreshTurnsSinceServed: 6}, switchTestMid, routingpolicy.SwitchReasonDowngradePending},
		{"cold downgrade moves", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestLow, CacheCold: true}, switchTestLow, routingpolicy.SwitchReasonDowngrade},
		{"upgrade to a model served last turn is free", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestMid, FreshTurnsSinceServed: 2}, switchTestMid, routingpolicy.SwitchReasonUpgradeFree},
		{"lone warm upgrade still waits for a second vote", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestMid}, switchTestLow, routingpolicy.SwitchReasonUpgradePending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.input.EstimatedInputTokens == 0 {
				tc.input.EstimatedInputTokens = 80_000
			}
			tc.input.PromptTokens = largePrompt
			verdict := routingpolicy.DecideSwitch(tc.input)
			assert.Equal(t, tc.serve, verdict.Serve)
			assert.Equal(t, tc.reason, verdict.Reason)
			assert.Equal(t, tc.serve != tc.input.Current, verdict.Switch)
		})
	}
}

func TestDecideSwitchSmallPromptDowngradeStillMovesAtOnce(t *testing.T) {
	verdict := routingpolicy.DecideSwitch(routingpolicy.SwitchInput{Current: switchTestHigh, Fresh: switchTestLow, EstimatedInputTokens: 10_000, PromptTokens: 10_000})
	assert.Equal(t, switchTestLow, verdict.Serve)
	assert.Equal(t, routingpolicy.SwitchReasonDowngrade, verdict.Reason)
}
