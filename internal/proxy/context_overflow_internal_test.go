package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContextWindowForRequest_ExtendedContextModelsReport1M is the premise for
// the overflow filter: a CapExtendedContext model always advertises 1M (the
// proxy injects the context-1m beta when it dispatches), while a 200K-only
// model reports its catalog window.
func TestContextWindowForRequest_ExtendedContextModelsReport1M(t *testing.T) {
	assert.Equal(t, 1_000_000, contextWindowForRequest("claude-opus-4-8"))
	assert.Equal(t, 1_000_000, contextWindowForRequest("claude-sonnet-4-6"))
	assert.Equal(t, 200_000, contextWindowForRequest("claude-haiku-4-5"))
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

	out, overflowed := excludeContextOverflowModels(250_000, 0, 8_000, nil, nil, available)

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

	out, overflowed := excludeContextOverflowModels(10_000, 0, 8_000, nil, nil, available)

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
	out, overflowed := excludeContextOverflowModels(260_000, 20_000, 8_000, nil, nil, available)

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
	s := &Service{availableModels: map[string]struct{}{"claude-haiku-4-5": {}}}

	// The routing-path filter, seeded with the policy exclusion, skips haiku (it
	// is already excluded) — so the overflow denylist it returns is empty.
	_, routingOverflowed := excludeContextOverflowModels(
		env.ContextOverflowTokenEstimate(), env.SignatureTokenSavings(), 8_000,
		nil, map[string]struct{}{"claude-haiku-4-5": {}}, s.availableModels,
	)
	assert.NotContains(t, routingOverflowed, "claude-haiku-4-5",
		"the routing filter skips a policy-excluded model — this is the gap safetyExcludedModels must close")

	// safetyExcludedModels re-runs against an empty base, so it DOES catch the
	// overflow regardless of policy exclusion.
	safety := s.safetyExcludedModels(env, 8_000, nil)
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
	outBoth, overflowedBoth := excludeContextOverflowModels(546_016, 0, 64_000, enabledBoth, nil, available)
	assert.NotContains(t, overflowedBoth, "deepseek/deepseek-v4-pro-0813")
	assert.NotContains(t, outBoth, "deepseek/deepseek-v4-pro-0813")

	_, overflowedFireworks := excludeContextOverflowModels(546_016, 0, 64_000, enabledFireworksOnly, nil, available)
	assert.NotContains(t, overflowedFireworks, "deepseek/deepseek-v4-pro-0813",
		"Fireworks serves the 1M window")

	// nil enabledProviders retains legacy model-level behavior.
	_, overflowedNil := excludeContextOverflowModels(546_016, 0, 64_000, nil, nil, available)
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
	ruledOut, overflowed := excludeContextOverflowModels(2_000_000, 0, 8_000, nil, nil, available)
	require.ElementsMatch(t, []string{"claude-opus-4-8", "claude-haiku-4-5"}, overflowed)
	assert.Equal(t, []string{"claude-opus-4-8"}, admitWidestOnTotalOverflow(ruledOut, overflowed, available, nil),
		"only the widest model is re-admitted")

	ruledOut, overflowed = excludeContextOverflowModels(250_000, 0, 8_000, nil, nil, available)
	assert.Nil(t, admitWidestOnTotalOverflow(ruledOut, overflowed, available, nil),
		"a request some model fits leaves the pre-filter's exclusions alone")

	policyExcluded := map[string]struct{}{"claude-opus-4-8": {}}
	ruledOut, overflowed = excludeContextOverflowModels(2_000_000, 0, 8_000, nil, policyExcluded, available)
	assert.Equal(t, []string{"claude-haiku-4-5"}, admitWidestOnTotalOverflow(ruledOut, overflowed, available, nil),
		"a policy-excluded model is never re-admitted; the widest allowed one is")
}

func TestWithoutModels(t *testing.T) {
	set := map[string]struct{}{"a": {}, "b": {}}
	out := withoutModels(set, []string{"a"})
	assert.Equal(t, map[string]struct{}{"b": {}}, out)
	assert.Len(t, set, 2, "the input set is never mutated")
	assert.Nil(t, withoutModels(nil, []string{"a"}))
}

func TestWithoutModelsKeep_PreservesIndependentExclusions(t *testing.T) {
	set := map[string]struct{}{"gemini-3.1-pro-preview": {}, "claude-haiku-4-5": {}}
	admitted := []string{"gemini-3.1-pro-preview"}
	unsigned := []string{"gemini-3.1-pro-preview"}
	out := withoutModelsKeep(set, admitted, unsigned)
	assert.Equal(t, map[string]struct{}{"gemini-3.1-pro-preview": {}, "claude-haiku-4-5": {}}, out,
		"unsigned-history exclusion survives overflow readmission")
	assert.Equal(t, map[string]struct{}{"claude-haiku-4-5": {}}, withoutModelsKeep(set, admitted, nil),
		"with no keep list, overflow readmission still drops the overflowed model")
}

func TestContextWindowOnlyExclusions_DropsAdmittedAndUnsigned(t *testing.T) {
	overflowed := []string{"claude-sonnet-4-5", "claude-opus-5", "gemini-3.1-pro-preview"}
	out := contextWindowOnlyExclusions(overflowed, []string{"claude-opus-5"}, []string{"gemini-3.1-pro-preview"})
	assert.Equal(t, map[string]struct{}{"claude-sonnet-4-5": {}}, out,
		"admitted models already serve and unsigned-history models stay out for their own reason")
	assert.Nil(t, contextWindowOnlyExclusions(nil, nil, nil))
}

func TestIsUpstreamContextOverflow_ProviderShapes(t *testing.T) {
	overflow := func(status int, body string) error {
		return &providers.UpstreamErrorResponse{Status: status, Body: []byte(body)}
	}
	for name, err := range map[string]error{
		"anthropic": overflow(400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1050000 tokens > 1000000 maximum"}}`),
		"openai":    overflow(400, `{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_length_exceeded"}}`),
		"gemini":    overflow(400, `{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`),
		"vllm":      overflow(400, `{"object":"error","message":"This model's maximum context length is 131072 tokens. However, you requested 140000 tokens."}`),
		"wrapped":   fmt.Errorf("attempt 2: %w", overflow(400, `{"error":{"code":"context_length_exceeded"}}`)),
	} {
		assert.True(t, isUpstreamContextOverflow(err), name)
		assert.True(t, isContextOverflow(err), name)
	}
	for name, err := range map[string]error{
		"rate limit status": overflow(429, `{"error":{"code":"context_length_exceeded"}}`),
		"rate limit body":   overflow(400, `{"error":{"message":"rate limit: too many tokens per minute; maximum context length unaffected"}}`),
		"server error":      overflow(500, `{"error":{"message":"prompt is too long"}}`),
		"other 400":         overflow(400, `{"error":{"message":"tools.0.name: invalid"}}`),
		"not buffered":      errors.New("prompt is too long"),
	} {
		assert.False(t, isUpstreamContextOverflow(err), name)
	}
	assert.True(t, isContextOverflow(fmt.Errorf("router: %w", policy.ErrContextWindowExceeded)))
}

func TestClassifyDispatchError_ContextOverflowIsNative(t *testing.T) {
	for name, err := range map[string]error{
		"router":   fmt.Errorf("wrapped: %w", policy.ErrContextWindowExceeded),
		"upstream": &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"code":"context_length_exceeded"}}`)},
	} {
		cls, ok := ClassifyDispatchError(err)
		require.True(t, ok, name)
		assert.Equal(t, DispatchErrorContextWindowExceeded, cls.Kind, name)
		assert.Equal(t, http.StatusBadRequest, cls.Status, name)
		assert.True(t, strings.HasPrefix(cls.Message, "prompt is too long"), "Claude Code and opencode key compaction off this wording")
		assert.True(t, cls.Kind.IsClientError(), name)
		assert.Equal(t, "context_length_exceeded", OpenAIErrorCode(cls.Kind))
	}
	assert.Empty(t, OpenAIErrorCode(DispatchErrorUpstreamStatus))
}

func TestFlushHelpersLeaveOverflowToHandler(t *testing.T) {
	overflow := &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"code":"context_length_exceeded"}}`)}
	for name, flush := range map[string]func(http.ResponseWriter, error){
		"buffered":  flushBufferedIfPresent,
		"anthropic": flushUpstreamErrorAsAnthropic,
	} {
		rec := httptest.NewRecorder()
		flush(rec, overflow)
		assert.Zero(t, rec.Body.Len(), "%s: the handler renders the client-native overflow", name)
		assert.False(t, rec.Flushed, name)

		rec = httptest.NewRecorder()
		flush(rec, &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"message":"bad tool"}}`)})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: other upstream errors still flush", name)
		assert.Contains(t, rec.Body.String(), "bad tool")
	}
}

func TestSSEErrorEventsCarryNativeOverflow(t *testing.T) {
	overflow := &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"message":"maximum context length is 131072 tokens"}}`)}

	rec := httptest.NewRecorder()
	_ = emitAnthropicSSEErrorEvent(rec, overflow)
	assert.Contains(t, rec.Body.String(), `"type":"invalid_request_error"`)
	assert.Contains(t, rec.Body.String(), `prompt is too long`)

	rec = httptest.NewRecorder()
	_ = emitOpenAISSEErrorEvent(rec, overflow)
	assert.Contains(t, rec.Body.String(), `"code":"context_length_exceeded"`)
	assert.Contains(t, rec.Body.String(), `prompt is too long`)
}
