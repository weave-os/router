package proxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"workweave/router/internal/router"
	"workweave/router/internal/router/planner"
	"workweave/router/internal/translate"
)

func TestRoutingMarkerFor_PlannerPaths(t *testing.T) {
	decision := router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: "openrouter"}

	cases := []struct {
		name           string
		res            turnLoopResult
		wantContains   []string
		wantNotContain []string
		wantEmpty      bool
	}{
		{
			name: "ev_positive: planner switched",
			res: turnLoopResult{
				Decision:        decision,
				PlannerDecision: planner.Decision{Reason: planner.ReasonEVPositive},
			},
			wantContains: []string{
				"✦ **Weave Router** → deepseek/deepseek-v4-pro · " + markerReasonSwitched,
			},
			wantNotContain: []string{
				"(openrouter)",
				"reason:",
				"ev_positive",
			},
		},
		{
			// Stay reasons only fire when the scorer/planner re-confirmed the same
			// model already serving this session — which is exactly the case the
			// switch-only gate now suppresses.
			name: "ev_negative: same model as last turn, marker suppressed",
			res: turnLoopResult{
				Decision:         decision,
				PlannerDecision:  planner.Decision{Reason: planner.ReasonEVNegative},
				PriorServedModel: decision.Model,
			},
			wantEmpty: true,
		},
		{
			name: "no_prior_usage: same model as last turn, marker suppressed",
			res: turnLoopResult{
				Decision:         decision,
				PlannerDecision:  planner.Decision{Reason: planner.ReasonNoPriorUsage},
				PriorServedModel: decision.Model,
			},
			wantEmpty: true,
		},
		{
			name: "same_tier_pinned: same model as last turn, marker suppressed",
			res: turnLoopResult{
				Decision:         decision,
				PlannerDecision:  planner.Decision{Reason: planner.ReasonSameTierPinned},
				PriorServedModel: decision.Model,
			},
			wantEmpty: true,
		},
		{
			name: "same_model: same model as last turn, marker suppressed",
			res: turnLoopResult{
				Decision:         decision,
				PlannerDecision:  planner.Decision{Reason: planner.ReasonSameModel},
				PriorServedModel: decision.Model,
			},
			wantEmpty: true,
		},
		{
			name: "tier_upgrade: planner bumped to a higher tier",
			res: turnLoopResult{
				Decision:        decision,
				PlannerDecision: planner.Decision{Reason: planner.ReasonTierUpgrade},
			},
			wantContains: []string{
				"· " + markerReasonTierUpgrade,
			},
		},
		{
			name: "pricing_missing: recovery code is silenced",
			res: turnLoopResult{
				Decision:        decision,
				PlannerDecision: planner.Decision{Reason: planner.ReasonPricingMissing},
			},
			wantContains: []string{
				"✦ **Weave Router** → deepseek/deepseek-v4-pro",
			},
			wantNotContain: []string{
				"·",
				"pricing",
				"missing",
			},
		},
		{
			name: "pin_model_missing: recovery code is silenced",
			res: turnLoopResult{
				Decision:        decision,
				PlannerDecision: planner.Decision{Reason: planner.ReasonPinModelMissing},
			},
			wantNotContain: []string{
				"·",
				"pin model",
			},
		},
		{
			name: "no planner ran, fresh decision: best-pick fallback",
			res: turnLoopResult{
				Decision: decision,
			},
			wantContains: []string{
				"· " + markerReasonBestPick,
			},
			wantNotContain: []string{
				"top scorer",
				"reason:",
			},
		},
		{
			name: "hard-pin path: marker suppressed regardless of prior model",
			res: turnLoopResult{
				Decision:   decision,
				HardPinned: true,
			},
			wantEmpty: true,
		},
		{
			name: "tool-result short-circuit: marker suppressed",
			res: turnLoopResult{
				Decision:         decision,
				StickyHit:        true,
				PriorServedModel: "deepseek/deepseek-v4-pro",
			},
			wantEmpty: true,
		},
		{
			name: "tool-result but model switched: marker shown despite sticky hit",
			res: turnLoopResult{
				Decision:         router.Decision{Model: "claude-haiku-4-5", Provider: "anthropic"},
				StickyHit:        true,
				PriorServedModel: "deepseek/deepseek-v4-pro",
			},
			wantContains: []string{
				"✦ **Weave Router** → claude-haiku-4-5 · " + markerReasonBestPick,
			},
			wantNotContain: []string{
				"(anthropic)",
			},
		},
		{
			name: "recovery code with model switch: marker shown without reason",
			res: turnLoopResult{
				Decision:         router.Decision{Model: "claude-haiku-4-5", Provider: "anthropic"},
				StickyHit:        true,
				PriorServedModel: "deepseek/deepseek-v4-pro",
				PlannerDecision:  planner.Decision{Reason: "pin_model_missing"},
			},
			wantContains: []string{
				"✦ **Weave Router** → claude-haiku-4-5\n\n",
			},
			wantNotContain: []string{
				"(anthropic)",
				"pin_model_missing",
				markerReasonBestPick,
			},
		},
		{
			name: "user-forced model: distinct marker, not best-pick",
			res: turnLoopResult{
				Decision:         router.Decision{Model: "gpt-5.5", Provider: "openai", Reason: translate.ReasonUserForceModel},
				StickyHit:        true,
				PriorServedModel: "deepseek/deepseek-v4-flash",
			},
			wantContains: []string{
				"✦ **Weave Router** → gpt-5.5",
				"· " + markerReasonUserForced,
			},
			wantNotContain: []string{
				markerReasonBestPick,
			},
		},
		{
			name: "loop escalation: distinct marker, not best-pick",
			res: turnLoopResult{
				Decision:         router.Decision{Model: "claude-opus-4-8", Provider: "anthropic", Reason: translate.ReasonLoopEscalation},
				StickyHit:        true,
				PriorServedModel: "deepseek/deepseek-v4-flash",
			},
			wantContains: []string{
				"✦ **Weave Router** → claude-opus-4-8",
				"· " + markerReasonLoopEscalated,
			},
			wantNotContain: []string{
				markerReasonBestPick,
			},
		},
		{
			// A forced session re-pins the same model on every turn, so once the
			// pin's LastServedModel catches up to the forced model the badge must
			// stay suppressed on subsequent sticky turns — otherwise the marker
			// re-emits on every tool-result follow-up. The user already saw the
			// "force-model applied" acknowledgment when the directive was issued.
			name: "user-forced same model on a sticky follow-up: marker suppressed",
			res: turnLoopResult{
				Decision:         router.Decision{Model: "gpt-5.5", Provider: "openai", Reason: translate.ReasonUserForceModel},
				StickyHit:        true,
				PriorServedModel: "gpt-5.5", // pin already serving the forced model
			},
			wantEmpty: true,
		},
		{
			name: "loop-escalated same model on a sticky follow-up: marker suppressed",
			res: turnLoopResult{
				Decision:         router.Decision{Model: "claude-opus-4-8", Provider: "anthropic", Reason: translate.ReasonLoopEscalation},
				StickyHit:        true,
				PriorServedModel: "claude-opus-4-8", // pin already serving the escalated model
			},
			wantEmpty: true,
		},
		{
			// The bug this guards: a /force-model pin that can't be served
			// silently reverted to the scorer, so the user kept trusting the
			// "force-model applied" ack while another model served the turn.
			name: "dropped forced pin: surfaced even when the model didn't change",
			res: turnLoopResult{
				Decision:            router.Decision{Model: "gpt-5.5", Provider: "openai"},
				PriorServedModel:    "gpt-5.5",
				ForcedPinDropped:    true,
				ForcedPinDropReason: "provider_not_enabled",
				ForcedPinModel:      "claude-opus-5",
			},
			wantContains: []string{
				"✦ **Weave Router** → gpt-5.5",
				markerReasonForcedPinDropped,
				"claude-opus-5",
			},
			wantNotContain: []string{
				markerReasonUserForced,
				"provider_not_enabled",
			},
		},
		{
			name: "dropped forced pin: suppressed in suggestion mode",
			res: turnLoopResult{
				Decision:         router.Decision{Model: "gpt-5.5", Provider: "openai"},
				SuggestionMode:   true,
				ForcedPinDropped: true,
				ForcedPinModel:   "claude-opus-5",
			},
			wantEmpty: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routingMarkerFor(tc.res)
			if tc.wantEmpty {
				assert.Empty(t, got)
				return
			}
			for _, want := range tc.wantContains {
				assert.Contains(t, got, want)
			}
			for _, nope := range tc.wantNotContain {
				assert.NotContains(t, got, nope)
			}
			assert.True(t, len(got) >= 2 && got[len(got)-2:] == "\n\n",
				"marker must terminate with a blank line")
		})
	}
}

func TestRoutingMarkerFor_EmptyDecisionEmitsNothing(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{Decision: router.Decision{}})
	assert.Empty(t, got)
}

func TestRoutingMarkerFor_SuggestionModeSuppressed(t *testing.T) {
	res := turnLoopResult{
		Decision:       router.Decision{Model: "gpt-5.5", Provider: "openai"},
		SuggestionMode: true,
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonSameModel,
		},
	}
	got := routingMarkerFor(res)
	assert.Empty(t, got, "suggestion-mode responses must not emit the routing badge")
}

func TestRoutingMarkerFor_DropsProviderEvenWhenSet(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{Model: "claude-haiku-4-5", Provider: "anthropic"},
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	})
	assert.Contains(t, got, "✦ **Weave Router** → claude-haiku-4-5 ·")
	assert.NotContains(t, got, "(anthropic)", "provider must not leak into the user-facing marker")
	assert.NotContains(t, got, "()")
	assert.Contains(t, got, "· "+markerReasonBestPick)
}

func TestRoutingMarkerFor_UsesSidecarDisplayMarker(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model:    "moonshotai/kimi-k2.7-code",
			Provider: "openrouter",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → Delegating work with moonshotai/kimi-k2.7-code\n↳ label: delegated_work",
			},
		},
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	})

	assert.Equal(t, "✦ **Weave Router** → Delegating work with moonshotai/kimi-k2.7-code\n↳ label: delegated_work\n\n", got)
}

func TestRoutingMarkerFor_SidecarDisplayMarkerStillRespectsSuggestionMode(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{
		SuggestionMode: true,
		Decision: router.Decision{
			Model: "moonshotai/kimi-k2.7-code",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → Delegating work with moonshotai/kimi-k2.7-code\n↳ label: delegated_work",
			},
		},
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	})

	assert.Empty(t, got)
}

func TestRoutingMarkerFor_RejectsMalformedSidecarDisplayMarker(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model: "moonshotai/kimi-k2.7-code",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "not a router marker\narbitrary sidecar text",
			},
		},
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	})

	assert.Contains(t, got, "✦ **Weave Router** → moonshotai/kimi-k2.7-code")
	assert.Contains(t, got, markerReasonBestPick)
	assert.NotContains(t, got, "arbitrary sidecar text")
}

func TestRoutingMarkerFor_ClampsSidecarDisplayMarker(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model: "moonshotai/kimi-k2.7-code",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → " + strings.Repeat("x", maxSidecarDisplayMarkerRunes+100),
			},
		},
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	})

	assert.LessOrEqual(t, len([]rune(strings.TrimSpace(got))), maxSidecarDisplayMarkerRunes)
	assert.Contains(t, got, "✦ **Weave Router** → ")
}

func TestRoutingMarkerFor_SidecarDisplayMarkerSuppressedWhenServedModelUnchanged(t *testing.T) {
	// The HMM sidecar renders `<model> · <cluster reason>` (e.g. "fast route
	// for this turn") on every /route call regardless of whether the served
	// model actually changed from the prior turn. Without this gate the same
	// sub-agent would see the badge repeated turn after turn.
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model: "deepseek/deepseek-v4-flash",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → deepseek/deepseek-v4-flash · fast route for this turn",
			},
		},
		PriorServedModel: "deepseek/deepseek-v4-flash",
	})
	assert.Empty(t, got, "sidecar marker must be suppressed when the served model did not change")
}

func TestRoutingMarkerFor_SidecarDisplayMarkerShownOnGenuineSwitch(t *testing.T) {
	// Sanity check the other side: when the served model genuinely changes
	// the sidecar marker still renders.
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model: "deepseek/deepseek-v4-flash",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → deepseek/deepseek-v4-flash · fast route for this turn",
			},
		},
		PriorServedModel: "claude-sonnet-5",
	})
	assert.Equal(t, "✦ **Weave Router** → deepseek/deepseek-v4-flash · fast route for this turn\n\n", got)
}

func TestBaselineRoutingMarkerFor_SuppressedWhenBaselineAlreadyServing(t *testing.T) {
	got := baselineRoutingMarkerFor(turnLoopResult{
		PriorServedModel: "claude-opus-4-8",
	}, "claude-opus-4-8")
	assert.Empty(t, got, "a failover that lands back on the model already serving must stay quiet")
}

func TestBaselineRoutingMarkerFor_ShowsOnGenuineSwitch(t *testing.T) {
	got := baselineRoutingMarkerFor(turnLoopResult{
		PriorServedModel: "deepseek/deepseek-v4-pro",
	}, "claude-opus-4-8")
	assert.Equal(t, "✦ **Weave Router** → claude-opus-4-8 · "+markerReasonBaseline+"\n\n", got)
}

func TestHumanReasonFromPlanner_UnknownCodeIsSilenced(t *testing.T) {
	// Unknown codes return empty so a new planner reason can't leak its
	// snake_case label into the user-facing marker.
	got := humanReasonFromPlanner("brand_new_reason_v9")
	assert.Empty(t, got)
}

func TestRoutingMarkerFor_SuppressesStickyTurnForAllClients(t *testing.T) {
	res := turnLoopResult{
		Decision:         router.Decision{Model: "gpt-5.6-terra", Provider: "openai"},
		StickyHit:        true,
		PriorServedModel: "gpt-5.6-terra",
	}
	assert.Empty(t, routingMarkerFor(res), "same-model suppression applies to Codex as well as Claude Code")
}

func TestRoutingMarkerFor_SuppressesEffortOnlyChange(t *testing.T) {
	res := turnLoopResult{
		Decision:         router.Decision{Model: "gpt-5.6-luna", Provider: "openai", Effort: "xhigh"},
		PriorServedModel: "gpt-5.6-luna:xhigh",
	}
	assert.Empty(t, routingMarkerFor(res), "changing effort must not repeat the model-choice marker")
}
