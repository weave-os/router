package translate_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const chatEffortModel = "gpt-5.6-luna"

func TestPrepareOpenAI_AnthropicToolFallbackDisablesReasoning(t *testing.T) {
	for _, effort := range []string{"", "high"} {
		t.Run("effort="+effort, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"user","content":"list files"}],"max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`)
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			opts := translate.EmitOptions{
				TargetModel: chatEffortModel, TargetProvider: providers.ProviderOpenAI,
				Capabilities: router.Lookup(chatEffortModel), ForceReasoningEffort: effort,
			}
			chat, err := env.PrepareOpenAI(http.Header{}, opts)
			require.NoError(t, err)
			assert.Equal(t, "none", gjson.GetBytes(chat.Body, "reasoning_effort").String())
			assert.Equal(t, "read_file", gjson.GetBytes(chat.Body, "tools.0.function.name").String())
			assert.Equal(t, providers.EndpointChatCompletions, chat.Endpoint)

			responses, err := env.PrepareOpenAIResponses(http.Header{}, opts)
			require.NoError(t, err)
			wantEffort := effort
			if wantEffort == "" {
				wantEffort = "low"
			}
			assert.Equal(t, wantEffort, gjson.GetBytes(responses.Body, "reasoning.effort").String())
			assert.Equal(t, providers.EndpointResponses, responses.Endpoint)
		})
	}
}

func TestPrepareOpenAI_StrippedServerToolsDoNotDisableReasoning(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"user","content":"search"}],"max_tokens":1024,"tools":[{"type":"web_search_20250305","name":"web_search"}]}`))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel: chatEffortModel, TargetProvider: providers.ProviderOpenAI,
		Capabilities: router.Lookup(chatEffortModel),
	})
	require.NoError(t, err)
	assert.False(t, gjson.GetBytes(prep.Body, "reasoning_effort").Exists())
	assert.Zero(t, gjson.GetBytes(prep.Body, "tools.#").Int())
}
