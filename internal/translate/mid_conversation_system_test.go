package translate_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prepareMidConversationAnthropic(t *testing.T, body []byte, model string) (map[string]any, http.Header) {
	t.Helper()
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prepared, err := envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:    model,
		TargetProvider: providers.ProviderAnthropic,
		Capabilities:   router.Lookup(model),
	})
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(prepared.Body, &decoded))
	return decoded, prepared.Headers
}

func TestAnthropicMidConversationToolChangesPreserveIncidentShape(t *testing.T) {
	messages := make([]any, 0, 71)
	messages = append(messages,
		map[string]any{"role": "user", "content": "start"},
		map[string]any{"role": "system", "content": "keep the cached prefix stable"},
	)
	for index := 2; index < 70; index++ {
		role := "assistant"
		if index%2 == 1 {
			role = "user"
		}
		messages = append(messages, map[string]any{"role": role, "content": fmt.Sprintf("turn %d", index)})
	}
	const deferredToolName = "deferred.tool"
	messages = append(messages, map[string]any{
		"role": "system",
		"content": []any{
			map[string]any{"type": "text", "text": "the deferred tool is now available"},
			map[string]any{
				"type": "tool_removal",
				"tool": map[string]any{"type": "tool_reference", "name": deferredToolName},
			},
			map[string]any{
				"type": "tool_addition",
				"tool": map[string]any{"type": "tool_reference", "name": deferredToolName},
			},
		},
	})
	body, err := json.Marshal(map[string]any{
		"model":      "claude-opus-5",
		"max_tokens": 1024,
		"tools": []any{map[string]any{
			"name":          deferredToolName,
			"defer_loading": true,
			"input_schema":  map[string]any{"type": "object"},
		}},
		"messages": messages,
	})
	require.NoError(t, err)

	request, headers := prepareMidConversationAnthropic(t, body, "claude-opus-5")
	preparedMessages := request["messages"].([]any)
	require.Len(t, preparedMessages, 71)
	assert.Equal(t, "system", preparedMessages[1].(map[string]any)["role"], "earlier system messages keep their original index")
	assert.Equal(t, "user", preparedMessages[69].(map[string]any)["role"])
	toolChangeMessage := preparedMessages[70].(map[string]any)
	assert.Equal(t, "system", toolChangeMessage["role"])
	toolChangeBlocks := toolChangeMessage["content"].([]any)
	require.Len(t, toolChangeBlocks, 3)
	toolRemoval := toolChangeBlocks[1].(map[string]any)
	toolAddition := toolChangeBlocks[2].(map[string]any)
	assert.Equal(t, "tool_removal", toolRemoval["type"])
	assert.Equal(t, "tool_addition", toolAddition["type"])
	assert.NotContains(t, toolRemoval, "cache_control", "router cache injection must stay before the system message")
	assert.NotContains(t, toolAddition, "cache_control", "router cache injection must stay before the system message")

	declaredTool := request["tools"].([]any)[0].(map[string]any)
	removedToolReference := toolRemoval["tool"].(map[string]any)
	toolReference := toolAddition["tool"].(map[string]any)
	assert.Equal(t, true, declaredTool["defer_loading"])
	assert.NotEqual(t, deferredToolName, declaredTool["name"], "invalid Anthropic tool names are aliased")
	assert.Equal(t, declaredTool["name"], removedToolReference["name"], "tool-removal references use the declaration alias")
	assert.Equal(t, declaredTool["name"], toolReference["name"], "tool-change references use the declaration alias")
	assert.Contains(t, headers.Get("anthropic-beta"), "mid-conversation-tool-changes-2026-07-01")
}

func TestAnthropicMidConversationOutputConfigPreservedWithBeta(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-1","max_tokens":1024,"messages":[{"role":"user","content":"start"},{"role":"system","content":"use more effort","output_config":{"effort":"high"}}]}`)

	request, headers := prepareMidConversationAnthropic(t, body, "claude-fable-5-1")
	message := request["messages"].([]any)[1].(map[string]any)
	assert.Equal(t, "system", message["role"])
	assert.Equal(t, map[string]any{"effort": "high"}, message["output_config"])
	assert.Contains(t, headers.Get("anthropic-beta"), "mid-conversation-output-config-2026-07-01")
}

func TestAnthropicTurnScopedSystemMessagePreservedWithoutCacheMutation(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"start"},{"role":"system","clear_at":"next_user_message","content":"answer briefly"}]}`)

	request, headers := prepareMidConversationAnthropic(t, body, "claude-opus-5")
	messages := request["messages"].([]any)
	turnScoped := messages[1].(map[string]any)
	assert.Equal(t, "system", turnScoped["role"])
	assert.Equal(t, "next_user_message", turnScoped["clear_at"])
	assert.Equal(t, "answer briefly", turnScoped["content"], "turn-scoped messages must remain byte-stable")
	assert.Contains(t, headers.Get("anthropic-beta"), "mid-conversation-system-clear-at-2026-08-21")

	precedingBlocks := messages[0].(map[string]any)["content"].([]any)
	assert.Equal(t, map[string]any{"type": "ephemeral"}, precedingBlocks[0].(map[string]any)["cache_control"])
}

func TestAnthropicMidConversationMessagesRejectUnsupportedModels(t *testing.T) {
	tests := []struct {
		name  string
		model string
		body  string
	}{
		{
			name:  "plain system message on Sonnet 5",
			model: "claude-sonnet-5",
			body:  `{"messages":[{"role":"user","content":"start"},{"role":"system","content":"use the new rule"}]}`,
		},
		{
			name:  "tool changes on Sonnet 5",
			model: "claude-sonnet-5",
			body:  `{"messages":[{"role":"user","content":"start"},{"role":"system","content":[{"type":"tool_removal","tool":{"type":"tool_reference","name":"old_tool"}}]}]}`,
		},
		{
			name:  "message output config on Opus 4.8",
			model: "claude-opus-4-8",
			body:  `{"messages":[{"role":"user","content":"start"},{"role":"system","content":"try harder","output_config":{"effort":"high"}}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope, err := translate.ParseAnthropic([]byte(test.body))
			require.NoError(t, err)
			_, err = envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{
				TargetModel:    test.model,
				TargetProvider: providers.ProviderAnthropic,
				Capabilities:   router.Lookup(test.model),
			})
			assert.ErrorIs(t, err, translate.ErrModelTranslationRequirementsIncompatible)
			assert.True(t, translate.IsIntrinsicallyIncompatible(err))
		})
	}
}

func TestAnthropicTurnScopedSystemMessageRejectsExplicitCacheControl(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"start"},{"role":"system","clear_at":"next_user_message","content":[{"type":"text","text":"briefly","cache_control":{"type":"ephemeral"}}]}]}`)
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	_, err = envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:    "claude-opus-5",
		TargetProvider: providers.ProviderAnthropic,
		Capabilities:   router.Lookup("claude-opus-5"),
	})
	assert.ErrorIs(t, err, translate.ErrAnthropicCacheControlInvalid)
}
