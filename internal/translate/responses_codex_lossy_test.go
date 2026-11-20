package translate

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPortableCodexLossyRescueDropsOpaqueHistory(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,
		"tools":[{"type":"function","name":"exec","parameters":{"type":"object","properties":{}}},{"type":"tool_search"}],
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]},
		{"type":"agent_message","author":"child","recipient":"parent","content":[{"type":"encrypted_content","encrypted_content":"opaque"}]},
		{"type":"agent_message","author":"child","recipient":"parent","content":[{"type":"input_text","text":"review done"},{"type":"encrypted_content","encrypted_content":"opaque"}]},
		{"type":"tool_search_call","call_id":"ts_1"},{"type":"tool_search_output","call_id":"ts_1"},
		{"type":"function_call_output","call_id":"","output":"tests passed"}]}`)

	native, err := ConvertResponsesToChatCompletionsWithOptions(body, ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	require.True(t, native.Requirements.NativeOnly)

	rescued, err := ConvertResponsesToChatCompletionsWithOptions(body, ResponsesConversionOptions{PortableCodex: true, LossyRescue: true})
	require.NoError(t, err)
	require.False(t, rescued.Requirements.NativeOnly)
	messages := gjson.GetBytes(rescued.Body, "messages").Array()
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Get("content").String())
	}
	require.Contains(t, contents, "review done")
	require.Contains(t, contents, "Tool output:\ntests passed")
	require.NotContains(t, string(rescued.Body), "opaque")
	require.NotContains(t, string(rescued.Body), "tool_search")
}

func TestPortableCodexLossyRescueKeepsOtherNativeOnlyItems(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"unknown_item"}]}`)
	rescued, err := ConvertResponsesToChatCompletionsWithOptions(body, ResponsesConversionOptions{PortableCodex: true, LossyRescue: true})
	require.NoError(t, err)
	require.True(t, rescued.Requirements.NativeOnly)
}
