package translate_test

import (
	"net/http"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const fastModeAnthropicBody = `{"model":"claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`

func TestFastMode_AnthropicSetsSpeedAndBeta(t *testing.T) {
	prep := prepareWithFallback(t, http.Header{}, fastModeAnthropicBody, translate.EmitOptions{
		TargetModel:    "claude-opus-5",
		TargetProvider: providers.ProviderAnthropic,
		FastMode:       true,
	})

	assert.Equal(t, "fast", gjson.GetBytes(prep.Body, "speed").String())
	assert.Contains(t, prep.Headers.Get("anthropic-beta"), "fast-mode-2026-02-01")
}

func TestFastMode_AnthropicOffLeavesRequestUntouched(t *testing.T) {
	prep := prepareWithFallback(t, http.Header{}, fastModeAnthropicBody, translate.EmitOptions{
		TargetModel:    "claude-opus-5",
		TargetProvider: providers.ProviderAnthropic,
	})

	assert.False(t, gjson.GetBytes(prep.Body, "speed").Exists())
	assert.NotContains(t, prep.Headers.Get("anthropic-beta"), "fast-mode")
}

func TestFastMode_AnthropicGatewayNeverGetsSpeed(t *testing.T) {
	prep := prepareWithFallback(t, http.Header{}, fastModeAnthropicBody, translate.EmitOptions{
		TargetModel:    "claude-opus-5",
		TargetProvider: providers.ProviderAnthropicGateway,
		FastMode:       true,
	})

	assert.False(t, gjson.GetBytes(prep.Body, "speed").Exists())
	assert.NotContains(t, prep.Headers.Get("anthropic-beta"), "fast-mode")
}

func TestFastMode_AnthropicDedupesClientBeta(t *testing.T) {
	in := http.Header{}
	in.Set("anthropic-beta", "fast-mode-2026-02-01")
	prep := prepareWithFallback(t, in, fastModeAnthropicBody, translate.EmitOptions{
		TargetModel:    "claude-opus-5",
		TargetProvider: providers.ProviderAnthropic,
		FastMode:       true,
	})

	assert.Equal(t, "fast-mode-2026-02-01", prep.Headers.Get("anthropic-beta"))
}

func prepareOpenAIFast(t *testing.T, opts translate.EmitOptions) (chat, responses []byte) {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(fastModeAnthropicBody))
	require.NoError(t, err)
	opts.Capabilities = router.Lookup(opts.TargetModel)
	chatPrep, err := env.PrepareOpenAI(http.Header{}, opts)
	require.NoError(t, err)
	respPrep, err := env.PrepareOpenAIResponses(http.Header{}, opts)
	require.NoError(t, err)
	return chatPrep.Body, respPrep.Body
}

func TestFastMode_OpenAISetsPriorityServiceTierOnBothSurfaces(t *testing.T) {
	chat, responses := prepareOpenAIFast(t, translate.EmitOptions{
		TargetModel:    "gpt-5.6-luna",
		TargetProvider: providers.ProviderOpenAI,
		FastMode:       true,
	})

	assert.Equal(t, "priority", gjson.GetBytes(chat, "service_tier").String())
	assert.Equal(t, "priority", gjson.GetBytes(responses, "service_tier").String())
}

func TestFastMode_OpenAIOffLeavesServiceTierUnset(t *testing.T) {
	chat, responses := prepareOpenAIFast(t, translate.EmitOptions{
		TargetModel:    "gpt-5.6-luna",
		TargetProvider: providers.ProviderOpenAI,
	})

	assert.False(t, gjson.GetBytes(chat, "service_tier").Exists())
	assert.False(t, gjson.GetBytes(responses, "service_tier").Exists())
}

func TestFastMode_OpenAICompatGatewaysNeverGetServiceTier(t *testing.T) {
	for _, provider := range []string{providers.ProviderOpenAIGateway, providers.ProviderOpenRouter, providers.ProviderFireworks} {
		chat, responses := prepareOpenAIFast(t, translate.EmitOptions{
			TargetModel:    "gpt-5.6-luna",
			TargetProvider: provider,
			FastMode:       true,
		})
		assert.False(t, gjson.GetBytes(chat, "service_tier").Exists(), provider)
		assert.False(t, gjson.GetBytes(responses, "service_tier").Exists(), provider)
	}
}
