package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Anthropic Messages requires max_tokens; we inject a per-model default when
// absent. defaultMaxOutputTokenCap is 8192, floored by per-model caps.

func TestAnthropicSameFormat_DefaultMaxTokensInjectedWhenAbsent(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`)
	opts := translate.EmitOptions{
		TargetModel:  "claude-opus-4-7",
		Capabilities: router.Lookup("claude-opus-4-7"),
	}
	out := parseAndEmit(t, body, "anthropic", opts)
	assert.Equal(t, float64(8192), out["max_tokens"])
}

func TestAnthropicSameFormat_ExistingMaxTokensUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}],"max_tokens":1024}`)
	opts := translate.EmitOptions{
		TargetModel:  "claude-opus-4-7",
		Capabilities: router.Lookup("claude-opus-4-7"),
	}
	out := parseAndEmit(t, body, "anthropic", opts)
	assert.Equal(t, float64(1024), out["max_tokens"])
}

func TestOpenAISameFormat_DefaultMaxTokensInjectedForNonReasoningTarget(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	opts := translate.EmitOptions{
		TargetModel:  "gpt-4o",
		Capabilities: router.Lookup("gpt-4o"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(8192), out["max_tokens"])
	assert.NotContains(t, out, "max_completion_tokens")
}

func TestOpenAISameFormat_DefaultMaxCompletionTokensInjectedForReasoningTarget(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	opts := translate.EmitOptions{
		TargetModel:  "o3",
		Capabilities: router.Lookup("o3"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(8192), out["max_completion_tokens"])
	assert.NotContains(t, out, "max_tokens")
}

// gpt-4-turbo's cap (4096) is below the global default (8192); default must
// floor to the model cap so we never inject a value the model rejects.
func TestOpenAISameFormat_DefaultRespectsLowerPerModelCap(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	opts := translate.EmitOptions{
		TargetModel:  "gpt-4-turbo",
		Capabilities: router.Lookup("gpt-4-turbo"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(4096), out["max_tokens"])
}

// gpt-4.1 caps at 32768 above the global default (8192); the global cap is
// the binding floor for the default.
func TestOpenAISameFormat_DefaultCappedByGlobalCap(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	opts := translate.EmitOptions{
		TargetModel:  "gpt-4.1",
		Capabilities: router.Lookup("gpt-4.1"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(8192), out["max_tokens"])
}

func TestOpenAISameFormat_DefaultNotInjectedWhenMaxTokensPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":512}`)
	opts := translate.EmitOptions{
		TargetModel:  "gpt-4o",
		Capabilities: router.Lookup("gpt-4o"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(512), out["max_tokens"])
}

func TestOpenAISameFormat_DefaultNotInjectedWhenMaxCompletionTokensPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":512}`)
	opts := translate.EmitOptions{
		TargetModel:  "o3",
		Capabilities: router.Lookup("o3"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(512), out["max_completion_tokens"])
	assert.NotContains(t, out, "max_tokens")
}

// Regression: pullMaxTokens previously hardcoded 4096; the per-model default replaces it.
func TestCrossFormat_OpenAIToAnthropic_DefaultMaxTokensInjectedWhenAbsent(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:  "claude-opus-4-7",
		Capabilities: router.Lookup("claude-opus-4-7"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, float64(8192), out["max_tokens"])
}

// Source omits max_tokens, non-reasoning target: injection populates max_tokens.
func TestCrossFormat_AnthropicToOpenAI_DefaultMaxTokensInjectedWhenAbsent(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "gpt-4o",
		Capabilities: router.Lookup("gpt-4o"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, float64(8192), out["max_tokens"])
}

// Reasoning target: injection then rename to max_completion_tokens.
func TestCrossFormat_AnthropicToOpenAI_DefaultMaxCompletionTokensForReasoning(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "o3",
		Capabilities: router.Lookup("o3"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, float64(8192), out["max_completion_tokens"])
	assert.NotContains(t, out, "max_tokens")
}

// Invariant: default-injection must not mutate the source body bytes.
func TestAnthropicSameFormat_DefaultInjectionPreservesSourceBytes(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`)
	original := make([]byte, len(body))
	copy(original, body)

	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	_, err = env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:  "claude-opus-4-7",
		Capabilities: router.Lookup("claude-opus-4-7"),
	})
	require.NoError(t, err)

	assert.Equal(t, original, body)
}

// Regression: an explicit max_tokens is clamped to modelMaxOutputTokens, whose
// absent-key zero value falls back to the global 8192. Kimi K3 really accepts
// 131072 output tokens, so without an entry a large request was silently
// truncated 16x.
func TestOpenAISameFormat_ExplicitMaxTokensClampsToKimiK3Ceiling(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":32000}`)
	opts := translate.EmitOptions{
		TargetModel:  "moonshotai/kimi-k3",
		Capabilities: router.Lookup("moonshotai/kimi-k3"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(32000), out["max_tokens"])
}

// Regression: qwen/qwen3.8-max was absent from modelMaxOutputTokens, so
// explicit max_tokens was clamped to the 8192 fallback instead of 64000.
func TestOpenAISameFormat_ExplicitMaxTokensNotClampedTo8192ForQwen38Max(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":32000}`)
	opts := translate.EmitOptions{
		TargetModel:  "qwen/qwen3.8-max",
		Capabilities: router.Lookup("qwen/qwen3.8-max"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(32000), out["max_tokens"])
}

// Regression: GPT-6 Astra supports 128K output, so its always-on reasoning
// must not be squeezed into the unlisted-model 8192-token fallback.
func TestOpenAISameFormat_ExplicitMaxTokensNotClampedTo8192ForGPT6Astra(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":64000}`)
	opts := translate.EmitOptions{
		TargetModel:  "gpt-6-astra",
		Capabilities: router.Lookup("gpt-6-astra"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(64000), out["max_completion_tokens"])
	assert.NotContains(t, out, "max_tokens")
}

// Regression: both GLM-5.3 arms missing from modelMaxOutputTokens; max_tokens was
// clamped to 8192 — always-on reasoning exhausts that budget before answering.
func TestOpenAISameFormat_ExplicitMaxTokensNotClampedTo8192ForGLM53(t *testing.T) {
	for _, model := range []string{"z-ai/glm-5.3", "z-ai/glm-5.3-flash"} {
		body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":64000}`)
		opts := translate.EmitOptions{
			TargetModel:  model,
			Capabilities: router.Lookup(model),
		}
		out := parseAndEmit(t, body, "openai", opts)
		assert.Equal(t, float64(64000), out["max_tokens"], model)
	}
}

// Regression: the Bedrock-primary Qwen arms must stay clamped at Bedrock's
// 16K output ceiling; a pass-through of Claude Code's 64000 would hard-400.
func TestOpenAISameFormat_BedrockQwenClampedAt16384Ceiling(t *testing.T) {
	for _, model := range []string{"qwen/qwen3-coder-next", "qwen/qwen3-235b-a22b-2507", "qwen/qwen3-next-80b-a3b-instruct"} {
		body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":64000}`)
		opts := translate.EmitOptions{
			TargetModel:  model,
			Capabilities: router.Lookup(model),
		}
		out := parseAndEmit(t, body, "openai", opts)
		assert.Equal(t, float64(16384), out["max_tokens"], model)
	}
}

// The flag ceiling (64000) is preserved — the clamp must not ask Qwen for
// more than its served output limit.
func TestOpenAISameFormat_Qwen38MaxClampsAt64000Ceiling(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":65536}`)
	opts := translate.EmitOptions{
		TargetModel:  "qwen/qwen3.8-max",
		Capabilities: router.Lookup("qwen/qwen3.8-max"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(64000), out["max_tokens"])
}

func TestOpenAISameFormat_GPT56ProClampsAt128000Ceiling(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna-pro", "gpt-5.6-sol-pro"} {
		body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":131072}`)
		opts := translate.EmitOptions{
			TargetModel:  model,
			Capabilities: router.Lookup(model),
		}
		out := parseAndEmit(t, body, "openai", opts)
		assert.Equal(t, float64(128000), out["max_completion_tokens"], model)
		assert.NotContains(t, out, "max_tokens", model)
	}
}

// Same shape on the Anthropic->OpenAI cross-format path (the actual Claude
// Code route): the explicit 64000 must not be clamped to 8192.
func TestCrossFormat_AnthropicToOpenAI_Qwen38MaxExplicitMaxTokensPassedThrough(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}],"max_tokens":64000}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "qwen/qwen3.8-max",
		Capabilities: router.Lookup("qwen/qwen3.8-max"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, float64(64000), out["max_tokens"])
}
