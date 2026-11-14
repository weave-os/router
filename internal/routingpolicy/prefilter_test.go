package routingpolicy_test

import (
	"strings"
	"testing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContextWindowForRequest_ExtendedContextModelsReport1M is the premise for
// the overflow filter: a CapExtendedContext model always advertises 1M (the
// proxy injects the context-1m beta when it dispatches), while a 200K-only
// model reports its catalog window.
func TestContextWindowForRequest_ExtendedContextModelsReport1M(t *testing.T) {
	assert.Equal(t, 1_000_000, routingpolicy.ContextWindow("claude-opus-4-8"))
	assert.Equal(t, 1_000_000, routingpolicy.ContextWindow("claude-sonnet-4-6"))
	assert.Equal(t, 200_000, routingpolicy.ContextWindow("claude-haiku-4-5"))
}

// TestExcludeContextOverflowModels_KeepsExtendedContextModel is the regression
// for the debug-session bug: a ~250K-token first request was dispatched to
// Opus at its 200K default and 400'd immediately. Opus must survive the
// pre-filter (it serves at 1M) while a true 200K-only model is excluded.
func TestExcludeContextOverflowModels_KeepsExtendedContextModel(t *testing.T) {
	available := map[string]struct{}{
		"claude-opus-4-8":  {},
		"claude-haiku-4-5": {},
	}

	out, overflowed := routingpolicy.ExcludeContextOverflowModels(250_000, 0, 8_000, nil, nil, available)

	assert.Contains(t, overflowed, "claude-haiku-4-5", "200K-only model overflows a 258K request")
	assert.NotContains(t, overflowed, "claude-opus-4-8", "extended-context model fits at 1M and must stay eligible")
	_, opusExcluded := out["claude-opus-4-8"]
	assert.False(t, opusExcluded, "Opus must not be added to the denylist")
}

// TestExcludeContextOverflowModels_NoOverflowUnderWindow leaves the denylist
// untouched when every model fits.
func TestExcludeContextOverflowModels_NoOverflowUnderWindow(t *testing.T) {
	available := map[string]struct{}{
		"claude-opus-4-8":  {},
		"claude-haiku-4-5": {},
	}

	out, overflowed := routingpolicy.ExcludeContextOverflowModels(10_000, 0, 8_000, nil, nil, available)

	assert.Empty(t, overflowed)
	assert.Nil(t, out, "no additions returns the original (nil) denylist unchanged")
}

// TestExcludeContextOverflowModels_SignatureSavingsOnlyForStrippingTargets is
// the regression for the review finding: base64 thought-signatures are stripped
// before dispatch to a non-Anthropic target but kept for an Anthropic
// passthrough. So the signature savings must be applied only to stripping
// (non-Anthropic-family) models. Here the raw estimate overflows both a 256K
// OSS model and a 200K Anthropic model; the savings pull the OSS model back
// under its window (it never receives the signatures) but must NOT rescue the
// Anthropic model (it does).
func TestExcludeContextOverflowModels_SignatureSavingsOnlyForStrippingTargets(t *testing.T) {
	available := map[string]struct{}{
		"moonshotai/kimi-k2.7": {}, // fireworks → OpenAI-compat, strips signatures, 262144 window
		"claude-haiku-4-5":     {}, // anthropic → keeps signatures, 200K window
	}

	// est+reserve = 268K overflows kimi's 262144 without savings; -20K savings = 248K fits.
	out, overflowed := routingpolicy.ExcludeContextOverflowModels(260_000, 20_000, 8_000, nil, nil, available)

	assert.NotContains(t, overflowed, "moonshotai/kimi-k2.7", "OSS target strips signatures, so the savings keep it under its 256K window")
	assert.Contains(t, overflowed, "claude-haiku-4-5", "Anthropic target keeps signatures, so the savings do not apply and it overflows 200K")
	_, kimiExcluded := out["moonshotai/kimi-k2.7"]
	assert.False(t, kimiExcluded, "stripping target must not be denylisted")
}

// TestSafetyExcludedModels_CatchesPolicyExcludedOverflow guards the bypass
// gap: the routing-path filter skips models already in excluded_models, so a
// both-policy-and-overflow model never lands on the routing denylist. The
// safety set re-runs against an empty base to close that gap.
func TestSafetyExcludedModels_CatchesPolicyExcludedOverflow(t *testing.T) {
	// A body large enough that ContextOverflowTokenEstimate (len/6) plus the
	// output reserve exceeds haiku's 200K window. ~1.3MB / 6 ≈ 217K > 200K.
	big := strings.Repeat("x", 1_300_000)
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"` + big + `"}]}`))
	require.NoError(t, err)

	// haiku-4-5 is a 200K-only model (no extended-context beta), so the big body
	// overflows it. It is also the requested model AND policy-excluded here.
	available := map[string]struct{}{"claude-haiku-4-5": {}}

	// The routing-path filter, seeded with the policy exclusion, skips haiku (it
	// is already excluded) — so the overflow denylist it returns is empty.
	_, routingOverflowed := routingpolicy.ExcludeContextOverflowModels(
		env.ContextOverflowTokenEstimate(), env.SignatureTokenSavings(), 8_000,
		nil, map[string]struct{}{"claude-haiku-4-5": {}}, available,
	)
	assert.NotContains(t, routingOverflowed, "claude-haiku-4-5",
		"the routing filter skips a policy-excluded model — this is the gap safetyExcludedModels must close")

	// safetyExcludedModels re-runs against an empty base, so it DOES catch the
	// overflow regardless of policy exclusion.
	safety := routingpolicy.SafetyExcludedModels(env, 8_000, nil, nil, available, nil)
	_, blocked := safety["claude-haiku-4-5"]
	assert.True(t, blocked, "a policy-excluded model that also overflows must land in the safety set so bypass blocks it")
}

// TestExcludeContextOverflowModels_UsesRoutableBinding verifies the first
// available catalog binding supplies the context window.
func TestExcludeContextOverflowModels_UsesRoutableBinding(t *testing.T) {
	available := map[string]struct{}{
		"deepseek/deepseek-v4-pro-0813": {},
	}
	enabledBoth := map[string]struct{}{
		providers.ProviderTogether:  {},
		providers.ProviderFireworks: {},
	}
	enabledFireworksOnly := map[string]struct{}{
		providers.ProviderFireworks: {},
	}

	// 546016 + 64K reserve is below Fireworks' 1M served window, so the
	// provider's old Together 512K limit must not exclude the model.
	outBoth, overflowedBoth := routingpolicy.ExcludeContextOverflowModels(546_016, 0, 64_000, enabledBoth, nil, available)
	assert.NotContains(t, overflowedBoth, "deepseek/deepseek-v4-pro-0813")
	assert.NotContains(t, outBoth, "deepseek/deepseek-v4-pro-0813")

	_, overflowedFireworks := routingpolicy.ExcludeContextOverflowModels(546_016, 0, 64_000, enabledFireworksOnly, nil, available)
	assert.NotContains(t, overflowedFireworks, "deepseek/deepseek-v4-pro-0813",
		"Fireworks serves the 1M window")

	// nil enabledProviders retains legacy model-level behavior.
	_, overflowedNil := routingpolicy.ExcludeContextOverflowModels(546_016, 0, 64_000, nil, nil, available)
	assert.NotContains(t, overflowedNil, "deepseek/deepseek-v4-pro-0813",
		"the catalog model-level window is 1M")
}

// TestAdmitWidestOnTotalOverflow pins the no-router-compaction contract: an
// estimate that rules out every model re-admits the largest-window ones so
// the upstream, not the ÷4 estimate, decides whether the request overflows.
func TestAdmitWidestOnTotalOverflow(t *testing.T) {
	available := map[string]struct{}{
		"claude-opus-4-8":  {},
		"claude-haiku-4-5": {},
	}
	ruledOut, overflowed := routingpolicy.ExcludeContextOverflowModels(2_000_000, 0, 8_000, nil, nil, available)
	require.ElementsMatch(t, []string{"claude-opus-4-8", "claude-haiku-4-5"}, overflowed)
	assert.Equal(t, []string{"claude-opus-4-8"}, routingpolicy.AdmitWidestOnTotalOverflow(ruledOut, overflowed, available, nil, router.RequestBindings{}),
		"only the widest model is re-admitted")

	ruledOut, overflowed = routingpolicy.ExcludeContextOverflowModels(250_000, 0, 8_000, nil, nil, available)
	assert.Nil(t, routingpolicy.AdmitWidestOnTotalOverflow(ruledOut, overflowed, available, nil, router.RequestBindings{}),
		"a request some model fits leaves the pre-filter's exclusions alone")

	policyExcluded := map[string]struct{}{"claude-opus-4-8": {}}
	ruledOut, overflowed = routingpolicy.ExcludeContextOverflowModels(2_000_000, 0, 8_000, nil, policyExcluded, available)
	assert.Equal(t, []string{"claude-haiku-4-5"}, routingpolicy.AdmitWidestOnTotalOverflow(ruledOut, overflowed, available, nil, router.RequestBindings{}),
		"a policy-excluded model is never re-admitted; the widest allowed one is")
}

// A wider model the request cannot reach (its provider is not enabled) must
// not count as fitting: every servable model overflowing is still a total
// overflow, and the servable widest go to the upstream.
func TestAdmitWidestOnTotalOverflow_IgnoresUnservableModels(t *testing.T) {
	available := map[string]struct{}{
		"claude-opus-5-5":  {},
		"claude-haiku-5-5": {},
		"gemini-3.5-flash": {},
	}
	anthropicOnly := map[string]struct{}{providers.ProviderAnthropic: {}}
	ruledOut, overflowed := routingpolicy.ExcludeContextOverflowModels(872_297, 0, 128_000, anthropicOnly, nil, available)
	require.ElementsMatch(t, []string{"claude-opus-5-5", "claude-haiku-5-5"}, overflowed,
		"the 1.05M Gemini window still fits the estimate")
	assert.ElementsMatch(t, []string{"claude-opus-5-5", "claude-haiku-5-5"},
		routingpolicy.AdmitWidestOnTotalOverflow(ruledOut, overflowed, available, anthropicOnly, router.RequestBindings{}))

	withGoogle := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderGoogle: {}}
	ruledOut, overflowed = routingpolicy.ExcludeContextOverflowModels(872_297, 0, 128_000, withGoogle, nil, available)
	assert.Nil(t, routingpolicy.AdmitWidestOnTotalOverflow(ruledOut, overflowed, available, withGoogle, router.RequestBindings{}),
		"a servable model that fits leaves the exclusions alone")

	customGateway := map[string][]string{"gemini-3.5-flash": {providers.ProviderOpenRouter}}
	withGateway := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenRouter: {}}
	ruledOut, overflowed = routingpolicy.ExcludeContextOverflowModels(872_297, 0, 128_000, withGateway, nil, available)
	assert.Nil(t, routingpolicy.AdmitWidestOnTotalOverflow(ruledOut, overflowed, available, withGateway, router.RequestBindings{Custom: customGateway}),
		"a configuration-declared binding makes the model servable")

	gatewayOnly := router.RequestBindings{
		Custom:   map[string][]string{"claude-opus-5-5": {providers.ProviderOpenRouter}},
		Gateways: map[string]struct{}{providers.ProviderOpenRouter: {}},
	}
	ruledOut, overflowed = routingpolicy.ExcludeContextOverflowModels(872_297, 0, 128_000, withGateway, nil, available)
	assert.Equal(t, []string{"claude-opus-5-5"}, routingpolicy.AdmitWidestOnTotalOverflow(ruledOut, overflowed, available, withGateway, gatewayOnly),
		"a gateway-exclusive request reaches only its aliases, so vendor-only models never count as fitting")
}

func TestWithoutModels(t *testing.T) {
	set := map[string]struct{}{"a": {}, "b": {}}
	out := routingpolicy.WithoutModels(set, []string{"a"})
	assert.Equal(t, map[string]struct{}{"b": {}}, out)
	assert.Len(t, set, 2, "the input set is never mutated")
	assert.Nil(t, routingpolicy.WithoutModels(nil, []string{"a"}))
}

func TestWithoutModelsKeep_PreservesIndependentExclusions(t *testing.T) {
	set := map[string]struct{}{"gemini-3.1-pro-preview": {}, "claude-haiku-4-5": {}}
	admitted := []string{"gemini-3.1-pro-preview"}
	unsigned := []string{"gemini-3.1-pro-preview"}
	out := routingpolicy.WithoutModelsKeep(set, admitted, unsigned)
	assert.Equal(t, map[string]struct{}{"gemini-3.1-pro-preview": {}, "claude-haiku-4-5": {}}, out,
		"unsigned-history exclusion survives overflow readmission")
	assert.Equal(t, map[string]struct{}{"claude-haiku-4-5": {}}, routingpolicy.WithoutModelsKeep(set, admitted, nil),
		"with no keep list, overflow readmission still drops the overflowed model")
}

func TestContextWindowOnlyExclusions_DropsAdmittedAndUnsigned(t *testing.T) {
	overflowed := []string{"claude-sonnet-4-5", "claude-opus-5", "gemini-3.1-pro-preview"}
	out := routingpolicy.ContextWindowOnlyExclusions(overflowed, []string{"claude-opus-5"}, []string{"gemini-3.1-pro-preview"})
	assert.Equal(t, map[string]struct{}{"claude-sonnet-4-5": {}}, out,
		"admitted models already serve and unsigned-history models stay out for their own reason")
	assert.Nil(t, routingpolicy.ContextWindowOnlyExclusions(nil, nil, nil))
}

func TestExcludeGemini3xOnUnsignedHistory_ExcludesGeminiKeepsOthers(t *testing.T) {
	available := map[string]struct{}{
		"gemini-3.1-pro-preview":        {},
		"gemini-3.1-flash-lite-preview": {},
		"claude-opus-4-8":               {},
		"deepseek/deepseek-v4-pro":      {},
	}
	env, err := translate.ParseAnthropic([]byte(`{"model":"x","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}]}]}`))
	require.NoError(t, err)

	out, added := routingpolicy.ExcludeGemini3xOnUnsignedHistory(env, map[string]struct{}{}, available)
	assert.ElementsMatch(t, []string{"gemini-3.1-flash-lite-preview", "gemini-3.1-pro-preview"}, added)
	_, geminiExcluded := out["gemini-3.1-pro-preview"]
	assert.True(t, geminiExcluded, "gemini-3.x is excluded when history carries unsigned tool calls")
	_, opusExcluded := out["claude-opus-4-8"]
	assert.False(t, opusExcluded, "non-gemini models stay eligible")
}

func TestExcludeGemini3xOnUnsignedHistory_SignedHistoryIsNoop(t *testing.T) {
	available := map[string]struct{}{"gemini-3.1-pro-preview": {}, "claude-opus-4-8": {}}
	env, err := translate.ParseAnthropic([]byte(`{"model":"x","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1__thought__c2ln","name":"Bash","input":{}}]}]}`))
	require.NoError(t, err)

	out, added := routingpolicy.ExcludeGemini3xOnUnsignedHistory(env, map[string]struct{}{}, available)
	assert.Nil(t, added, "signed (native Gemini) history must not exclude Gemini")
	assert.Empty(t, out)
}

func TestGemini3xRequiresSignedHistory(t *testing.T) {
	assert.True(t, routingpolicy.Gemini3xRequiresSignedHistory("gemini-3.1-pro-preview"))
	assert.True(t, routingpolicy.Gemini3xRequiresSignedHistory("gemini-3.5-flash"))
	assert.True(t, routingpolicy.Gemini3xRequiresSignedHistory(router.ModelGemini4Argon))
	assert.False(t, routingpolicy.Gemini3xRequiresSignedHistory("gemini-2.5-flash"))
	assert.False(t, routingpolicy.Gemini3xRequiresSignedHistory("claude-opus-4-8"))
}

func TestExcludeImageLimitModels(t *testing.T) {
	model := "xiaomi/mimo-v2.6-pro"
	textOnly := "qwen/qwen3-235b-a22b-2507"
	available := map[string]struct{}{model: {}, textOnly: {}}

	t.Run("deepinfra only excludes capped model but keeps text-only soft filter", func(t *testing.T) {
		excluded, removed := routingpolicy.ExcludeImageLimitModels(17, map[string]struct{}{providers.ProviderDeepInfra: {}}, nil, nil, available)
		assert.Equal(t, []string{model}, removed)
		assert.Equal(t, map[string]struct{}{model: {}}, excluded)
	})
	t.Run("openrouter binding keeps model eligible", func(t *testing.T) {
		excluded, removed := routingpolicy.ExcludeImageLimitModels(17, map[string]struct{}{providers.ProviderOpenRouter: {}}, nil, nil, available)
		assert.Nil(t, excluded)
		assert.Empty(t, removed)
	})
	t.Run("custom binding has no known cap", func(t *testing.T) {
		excluded, removed := routingpolicy.ExcludeImageLimitModels(
			17,
			map[string]struct{}{providers.ProviderDeepInfra: {}},
			map[string][]string{model: {"custom"}},
			nil,
			available,
		)
		assert.Nil(t, excluded)
		assert.Empty(t, removed)
	})
	t.Run("existing exclusions are preserved copy-on-write", func(t *testing.T) {
		existing := map[string]struct{}{"already-excluded": {}}
		excluded, removed := routingpolicy.ExcludeImageLimitModels(
			17,
			map[string]struct{}{providers.ProviderDeepInfra: {}},
			nil,
			existing,
			map[string]struct{}{model: {}},
		)
		assert.Equal(t, map[string]struct{}{"already-excluded": {}, model: {}}, excluded)
		assert.Equal(t, map[string]struct{}{"already-excluded": {}}, existing)
		assert.Equal(t, []string{model}, removed)
	})
}

func TestSafetyExcludedModelsIncludesImageLimits(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"one"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"two"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"three"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"four"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"five"}}
	]}]}`))
	require.NoError(t, err)
	available := map[string]struct{}{"xiaomi/mimo-v2.6-pro": {}}
	excluded := routingpolicy.SafetyExcludedModels(env, 0, map[string]struct{}{providers.ProviderDeepInfra: {}}, nil, available, nil)
	assert.Equal(t, map[string]struct{}{"xiaomi/mimo-v2.6-pro": {}}, excluded)
}
