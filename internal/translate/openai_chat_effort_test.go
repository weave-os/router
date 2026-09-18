package translate_test

import (
	"fmt"
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
	for _, tc := range []struct {
		name                 string
		forceReasoningEffort string
		forceEffort          string
		wantResponsesEffort  string
	}{
		{name: "default", wantResponsesEffort: "low"},
		{name: "routing effort", forceReasoningEffort: "high", wantResponsesEffort: "high"},
		{name: "user effort", forceReasoningEffort: "high", forceEffort: "high", wantResponsesEffort: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"user","content":"list files"}],"max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`)
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			opts := translate.EmitOptions{
				TargetModel: chatEffortModel, TargetProvider: providers.ProviderOpenAI,
				Capabilities: router.Lookup(chatEffortModel), ForceReasoningEffort: tc.forceReasoningEffort,
				ForceEffort: tc.forceEffort,
			}
			chat, err := env.PrepareOpenAI(http.Header{}, opts)
			require.NoError(t, err)
			assert.Equal(t, "none", gjson.GetBytes(chat.Body, "reasoning_effort").String())
			assert.Equal(t, int64(1024), gjson.GetBytes(chat.Body, "max_completion_tokens").Int())
			assert.Equal(t, "read_file", gjson.GetBytes(chat.Body, "tools.0.function.name").String())
			assert.Equal(t, providers.EndpointChatCompletions, chat.Endpoint)

			responses, err := env.PrepareOpenAIResponses(http.Header{}, opts)
			require.NoError(t, err)
			assert.Equal(t, tc.wantResponsesEffort, gjson.GetBytes(responses.Body, "reasoning.effort").String())
			assert.Equal(t, int64(16000), gjson.GetBytes(responses.Body, "max_output_tokens").Int())
			assert.Equal(t, providers.EndpointResponses, responses.Endpoint)
		})
	}
}

func TestPrepareOpenAI_AnthropicToolFallbackPreservesOutputBudget(t *testing.T) {
	for _, tc := range []struct {
		requested int64
		want      int64
	}{
		{requested: 64, want: 64},
		{requested: 1024, want: 1024},
		{requested: 32000, want: 32000},
		{requested: 200000, want: 128000},
	} {
		t.Run(fmt.Sprint(tc.requested), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"list files"}],"max_tokens":%d,"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`, tc.requested))
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
				TargetModel: chatEffortModel, TargetProvider: providers.ProviderOpenAI,
				Capabilities: router.Lookup(chatEffortModel),
			})
			require.NoError(t, err)
			assert.Equal(t, "none", gjson.GetBytes(prep.Body, "reasoning_effort").String())
			assert.Equal(t, tc.want, gjson.GetBytes(prep.Body, "max_completion_tokens").Int())
			assert.False(t, gjson.GetBytes(prep.Body, "max_tokens").Exists())
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
	assert.Equal(t, int64(16000), gjson.GetBytes(prep.Body, "max_completion_tokens").Int())
	assert.Zero(t, gjson.GetBytes(prep.Body, "tools.#").Int())
}
