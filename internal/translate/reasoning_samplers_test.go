package translate_test

import (
	"encoding/json"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// samplerFields reports presence of temperature/top_p on an emitted body.
func samplerFields(t *testing.T, body []byte) (map[string]any, bool, bool) {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	_, hasTemp := doc["temperature"]
	_, hasTopP := doc["top_p"]
	return doc, hasTemp, hasTopP
}

func TestReasoningSamplers_AnthropicSource_DropsTemperatureForGPT5(t *testing.T) {
	// Claude Code's Bash-prefix-detection turn: no tools (so chat/completions,
	// not Responses) and temperature 0, which gpt-5.x rejects outright.
	src := []byte(`{
		"model":"claude-3-5-haiku-20241022",
		"messages":[{"role":"user","content":"hi"}],
		"temperature":0,
		"top_p":0.9,
		"max_tokens":256
	}`)
	env, err := translate.ParseAnthropic(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:    "gpt-5.6-luna",
		TargetProvider: providers.ProviderOpenAI,
		Capabilities:   router.Lookup("gpt-5.6-luna"),
	})
	require.NoError(t, err)

	doc, hasTemp, hasTopP := samplerFields(t, out.Body)
	assert.False(t, hasTemp, "gpt-5.x reasoning targets only accept the default temperature")
	assert.False(t, hasTopP, "gpt-5.x reasoning targets only accept the default top_p")
	assert.Equal(t, "gpt-5.6-luna", doc["model"])
}

func TestReasoningSamplers_AnthropicSource_KeepsTemperatureForNonGPT5(t *testing.T) {
	src := []byte(`{
		"model":"claude-3-5-haiku-20241022",
		"messages":[{"role":"user","content":"hi"}],
		"temperature":0,
		"max_tokens":256
	}`)
	env, err := translate.ParseAnthropic(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:    "gpt-4.1",
		TargetProvider: providers.ProviderOpenAI,
		Capabilities:   router.Lookup("gpt-4.1"),
	})
	require.NoError(t, err)

	_, hasTemp, _ := samplerFields(t, out.Body)
	assert.True(t, hasTemp, "non-reasoning targets keep the client's temperature")
}

func TestReasoningSamplers_OpenAISource_DropsTemperatureForGPT5(t *testing.T) {
	src := []byte(`{
		"model":"gpt-5.6-luna",
		"messages":[{"role":"user","content":"hi"}],
		"temperature":0,
		"top_p":0.5
	}`)
	env, err := translate.ParseOpenAI(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:    "gpt-5.6-luna",
		TargetProvider: providers.ProviderOpenAI,
		Capabilities:   router.Lookup("gpt-5.6-luna"),
	})
	require.NoError(t, err)

	_, hasTemp, hasTopP := samplerFields(t, out.Body)
	assert.False(t, hasTemp)
	assert.False(t, hasTopP)
}

func TestReasoningSamplers_OpenAISource_KeepsTemperatureForOSSReasoning(t *testing.T) {
	src := []byte(`{
		"model":"x",
		"messages":[{"role":"user","content":"hi"}],
		"temperature":0
	}`)
	env, err := translate.ParseOpenAI(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:  "deepseek/deepseek-v4-pro",
		Capabilities: router.NewSpec(router.CapReasoning),
	})
	require.NoError(t, err)

	_, hasTemp, _ := samplerFields(t, out.Body)
	assert.True(t, hasTemp, "OpenRouter OSS reasoning models sample normally")
}
