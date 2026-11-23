package routingpolicy_test

import (
	"testing"
	"time"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"

	"github.com/stretchr/testify/assert"
)

var switchTestNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

const (
	switchTestLow     = "claude-haiku-5-5"
	switchTestMid     = "claude-sonnet-5-5"
	switchTestHigh    = "claude-opus-5-5"
	switchTestUnknown = "synthetic-unpriced-model"

	switchTestPrefixCached = "gpt-6.1-sol"
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
		{"cheaper pick on a small prompt moves", routingpolicy.SwitchInput{Current: switchTestHigh, Fresh: switchTestLow}, switchTestLow, routingpolicy.SwitchReasonDowngrade},
		{"pricier pick on a small prompt moves", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh}, switchTestHigh, routingpolicy.SwitchReasonUpgrade},
		{"pricier pick on a cold cache moves", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh, CacheCold: true}, switchTestHigh, routingpolicy.SwitchReasonUpgrade},
		{"pricier pick on a broken prefix moves", routingpolicy.SwitchInput{Current: switchTestLow, Fresh: switchTestHigh, PrefixBroken: true}, switchTestHigh, routingpolicy.SwitchReasonUpgrade},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.input.EstimatedInputTokens = 10_000
			tc.input.Now = switchTestNow
			verdict := routingpolicy.DecideSwitch(tc.input)
			assert.Equal(t, tc.serve, verdict.Serve)
			assert.Equal(t, tc.reason, verdict.Reason)
			assert.Equal(t, tc.serve != tc.input.Current, verdict.Switch)
		})
	}
}

func TestDecideSwitchPricesTheRewriteOfALargePrompt(t *testing.T) {
	const largePrompt = 600_000
	cases := []struct {
		name   string
		input  routingpolicy.SwitchInput
		serve  string
		reason routingpolicy.SwitchReason
	}{
		{"side request is served on the fresh pick without moving the session", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestLow, SideRequest: true}, switchTestLow, routingpolicy.SwitchReasonSideRequest},
		{"downgrade into an unseen destination holds while the current cache is warm", routingpolicy.SwitchInput{Current: switchTestHigh, Fresh: switchTestMid}, switchTestHigh, routingpolicy.SwitchReasonDowngradePending},
		{"downgrade to a model served last turn moves", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestLow, MessageCount: 100, ModelCaches: sessionpin.ModelCaches{switchTestLow: {ServedAt: switchTestNow.Add(-20 * time.Second), PromptTokens: 599_000, MessageCount: 98}}}, switchTestLow, routingpolicy.SwitchReasonDowngrade},
		{"cold downgrade moves", routingpolicy.SwitchInput{Current: switchTestHigh, Fresh: switchTestMid, CacheCold: true}, switchTestMid, routingpolicy.SwitchReasonDowngrade},
		{"broken prefix downgrade moves", routingpolicy.SwitchInput{Current: switchTestHigh, Fresh: switchTestMid, PrefixBroken: true}, switchTestMid, routingpolicy.SwitchReasonDowngrade},
		{"cold upgrade moves", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestHigh, CacheCold: true}, switchTestHigh, routingpolicy.SwitchReasonUpgrade},
		{"broken prefix upgrade moves", routingpolicy.SwitchInput{Current: switchTestMid, Fresh: switchTestHigh, PrefixBroken: true}, switchTestHigh, routingpolicy.SwitchReasonUpgrade},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.input.EstimatedInputTokens = 80_000
			tc.input.PromptTokens = largePrompt
			tc.input.Now = switchTestNow
			verdict := routingpolicy.DecideSwitch(tc.input)
			assert.Equal(t, tc.serve, verdict.Serve)
			assert.Equal(t, tc.reason, verdict.Reason)
			assert.Equal(t, tc.serve != tc.input.Current && !tc.input.SideRequest, verdict.Switch)
		})
	}
}
