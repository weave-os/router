package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/turntype"
	"workweave/router/internal/translate"
)

// newHarnessEscalationSvc wires a Service with just the pieces
// applyHarnessEscalation touches.
func newHarnessEscalationSvc() *Service {
	return NewService(nil, nil, nil, false, nil, newStubPinStore(), false, "anthropic", "claude-haiku-4-5", nil)
}

func TestApplyHarnessEscalation_EscalatesLowTierNonClaudeOnEachHarnessTurnType(t *testing.T) {
	for _, tt := range []turntype.TurnType{turntype.HarnessMeta, turntype.SubAgentHarnessMeta, turntype.Recovery} {
		t.Run(string(tt), func(t *testing.T) {
			svc := newHarnessEscalationSvc()
			res := turnLoopResult{
				TurnType: tt,
				Decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: "best_pick"},
			}
			svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

			assert.Equal(t, escalateModel, res.Decision.Model)
			assert.Equal(t, providers.ProviderAnthropic, res.Decision.Provider)
			assert.Equal(t, translate.ReasonHarnessEscalation, res.Decision.Reason)
			assert.True(t, res.HarnessEscalated, "clamp must flag the rewrite")
		})
	}
}

func TestApplyHarnessEscalation_NonHarnessTurnTypeUntouched(t *testing.T) {
	for _, tt := range []turntype.TurnType{turntype.MainLoop, turntype.ToolResult, turntype.SubAgentDispatch, turntype.Compaction, turntype.Probe, turntype.TitleGen, turntype.Classifier} {
		t.Run(string(tt), func(t *testing.T) {
			svc := newHarnessEscalationSvc()
			original := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: "best_pick"}
			res := turnLoopResult{TurnType: tt, Decision: original}
			svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

			assert.Equal(t, original, res.Decision, "clamp must not engage for a non-harness turn type")
			assert.False(t, res.HarnessEscalated)
		})
	}
}

func TestApplyHarnessEscalation_AlreadyStrongClaudeTierHighUntouched(t *testing.T) {
	svc := newHarnessEscalationSvc()
	original := router.Decision{Provider: providers.ProviderAnthropic, Model: escalateModel, Reason: "best_pick"}
	res := turnLoopResult{TurnType: turntype.HarnessMeta, Decision: original}
	svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

	assert.Equal(t, original, res.Decision, "already on a strong Claude-family model -> no-op")
	assert.False(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_ClaudeMidTierStillEscalates(t *testing.T) {
	// claude-sonnet-4-6 is Claude-family but TierMid, not TierHigh — the clamp
	// must still route up rather than treating any Claude model as strong enough.
	svc := newHarnessEscalationSvc()
	res := turnLoopResult{
		TurnType: turntype.HarnessMeta,
		Decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-6", Reason: "best_pick"},
	}
	svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

	assert.Equal(t, escalateModel, res.Decision.Model, "Claude family but below TierHigh must still escalate")
	assert.True(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_NonClaudeTierHighStillEscalates(t *testing.T) {
	// gpt-5 is TierHigh but not Claude-family — the clamp cares about family,
	// not just tier, since a non-Anthropic upstream can still hallucinate
	// harness primitives.
	svc := newHarnessEscalationSvc()
	res := turnLoopResult{
		TurnType: turntype.HarnessMeta,
		Decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5", Reason: "best_pick"},
	}
	svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

	assert.Equal(t, escalateModel, res.Decision.Model, "TierHigh but non-Claude-family must still escalate")
	assert.True(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_UsageBypassOutranksClamp(t *testing.T) {
	svc := newHarnessEscalationSvc()
	original := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: "best_pick"}
	res := turnLoopResult{TurnType: turntype.HarnessMeta, Decision: original, UsageBypass: true}
	svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

	assert.Equal(t, original, res.Decision, "subscription usage-bypass outranks the clamp")
	assert.False(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_HardPinnedOutranksClamp(t *testing.T) {
	svc := newHarnessEscalationSvc()
	original := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: "best_pick"}
	res := turnLoopResult{TurnType: turntype.HarnessMeta, Decision: original, HardPinned: true}
	svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

	assert.Equal(t, original, res.Decision, "an operator/planner hard pin outranks the clamp")
	assert.False(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_UserForcedOutranksClamp(t *testing.T) {
	svc := newHarnessEscalationSvc()
	original := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: translate.ReasonUserForceModel}
	res := turnLoopResult{TurnType: turntype.HarnessMeta, Decision: original}
	svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

	assert.Equal(t, original, res.Decision, "a /force-model pin outranks the clamp")
	assert.False(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_AlreadyLoopEscalatedOutranksClamp(t *testing.T) {
	svc := newHarnessEscalationSvc()
	original := router.Decision{Provider: providers.ProviderAnthropic, Model: escalateModel, Reason: translate.ReasonLoopEscalation}
	res := turnLoopResult{TurnType: turntype.Recovery, Decision: original}
	svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

	assert.Equal(t, original, res.Decision, "an existing loop-escalation decision is already a stronger rescue")
	assert.False(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_KillSwitchDisablesClamp(t *testing.T) {
	svc := newHarnessEscalationSvc().WithHarnessEscalationConfig(false)
	original := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: "best_pick"}
	res := turnLoopResult{TurnType: turntype.HarnessMeta, Decision: original}
	svc.applyHarnessEscalation(context.Background(), &res, router.Request{})

	assert.Equal(t, original, res.Decision, "ROUTER_HARNESS_ESCALATION_ENABLED=false must suppress the clamp")
	assert.False(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_ProviderIneligibleLeavesDecisionUntouched(t *testing.T) {
	svc := newHarnessEscalationSvc()
	original := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: "best_pick"}
	res := turnLoopResult{TurnType: turntype.HarnessMeta, Decision: original}
	req := router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}
	svc.applyHarnessEscalation(context.Background(), &res, req)

	assert.Equal(t, original, res.Decision, "anthropic not in EnabledProviders -> escalate target unservable")
	assert.False(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_ModelExcludedLeavesDecisionUntouched(t *testing.T) {
	svc := newHarnessEscalationSvc()
	original := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: "best_pick"}
	res := turnLoopResult{TurnType: turntype.HarnessMeta, Decision: original}
	req := router.Request{ExcludedModels: map[string]struct{}{escalateModel: {}}}
	svc.applyHarnessEscalation(context.Background(), &res, req)

	assert.Equal(t, original, res.Decision, "escalateModel excluded -> original decision stands")
	assert.False(t, res.HarnessEscalated)
}

func TestApplyHarnessEscalation_NilEnabledProvidersIsUnrestricted(t *testing.T) {
	// A nil EnabledProviders map means "no restriction" per router.Request
	// semantics — the clamp must not treat nil as "anthropic disabled".
	svc := newHarnessEscalationSvc()
	res := turnLoopResult{
		TurnType: turntype.HarnessMeta,
		Decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: "best_pick"},
	}
	svc.applyHarnessEscalation(context.Background(), &res, router.Request{EnabledProviders: nil})

	assert.Equal(t, escalateModel, res.Decision.Model, "nil EnabledProviders must not block escalation")
	assert.True(t, res.HarnessEscalated)
}

// TestIsHardPinnedTurn_SubAgentHarnessMetaNeverHardPinned guards the
// isHardPinnedTurn branch added alongside the clamp: even with the legacy
// hardPinExplore switch AND a sub-agent override both configured,
// SubAgentHarnessMeta must never take the hard-pin short-circuit — it needs
// to reach the scorer/planner so applyHarnessEscalation (not a hard pin) can
// decide the model.
func TestIsHardPinnedTurn_SubAgentHarnessMetaNeverHardPinned(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, newStubPinStore(), true, "anthropic", "claude-haiku-4-5", nil).
		WithSubAgentOverride("anthropic", "claude-haiku-4-5")

	require.True(t, svc.hasSubAgentOverride())
	assert.False(t, svc.isHardPinnedTurn(context.Background(), turntype.SubAgentHarnessMeta))
	// Sanity: plain SubAgentDispatch DOES hard-pin under this same config, so
	// the exemption above is specific to the harness variant, not a config bug.
	assert.True(t, svc.isHardPinnedTurn(context.Background(), turntype.SubAgentDispatch))
}

func TestAuthoritativePolicyTurn_HarnessVariantsMatchUnderlyingShape(t *testing.T) {
	assert.Equal(t, authoritativePolicyTurn(turntype.MainLoop), authoritativePolicyTurn(turntype.HarnessMeta),
		"HarnessMeta must match MainLoop's authoritative-policy behavior")
	assert.Equal(t, authoritativePolicyTurn(turntype.ToolResult), authoritativePolicyTurn(turntype.Recovery),
		"Recovery must match ToolResult's authoritative-policy behavior")
	assert.False(t, authoritativePolicyTurn(turntype.SubAgentHarnessMeta),
		"SubAgentHarnessMeta's base (SubAgentDispatch) is not an authoritative-policy turn")
}
