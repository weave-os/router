package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGLM53FlashFlags_OpenRouter_ToolStreamSet(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:    "z-ai/glm-5.3-flash",
		TargetProvider: providers.ProviderOpenRouter,
		Capabilities:   router.Lookup("z-ai/glm-5.3-flash"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, true, out["tool_stream"], "glm-5.3-flash must receive tool_stream=true")
	_, hasKwargs := out["chat_template_kwargs"]
	assert.False(t, hasKwargs, "glm-5.3-flash has no vLLM binding, so no chat_template_kwargs")
	_, hasReasoning := out["reasoning"]
	assert.False(t, hasReasoning, "glm-5.3-flash thinking can't be disabled, so no reasoning hint")
}

func TestGLM53FlashFlags_AnthropicCrossFormat_ToolStreamSet(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-7",
		"max_tokens": 256,
		"messages": [{"role":"user","content":"hi"}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:    "z-ai/glm-5.3-flash",
		TargetProvider: providers.ProviderOpenRouter,
		Capabilities:   router.Lookup("z-ai/glm-5.3-flash"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, true, out["tool_stream"], "anthropic→openrouter glm-5.3-flash must receive tool_stream=true")
}

func TestGLM53FlashFlags_ClientSetToolStreamPreserved(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tool_stream":false}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:    "z-ai/glm-5.3-flash",
		TargetProvider: providers.ProviderOpenRouter,
		Capabilities:   router.Lookup("z-ai/glm-5.3-flash"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, false, out["tool_stream"], "client-set tool_stream=false must be preserved")
}

func TestGLM53FlashFlags_NotAppliedToOtherModels(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:    "z-ai/glm-5",
		TargetProvider: providers.ProviderOpenRouter,
		Capabilities:   router.Lookup("z-ai/glm-5"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	_, hasToolStream := out["tool_stream"]
	assert.False(t, hasToolStream, "glm-5 must not receive glm-5.3-flash's tool_stream injection")
}
