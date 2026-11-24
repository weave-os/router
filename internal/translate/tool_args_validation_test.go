package translate_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

// OpenAI-compat upstreams (vLLM/SGLang) sometimes emit malformed JSON in
// tool_call.function.arguments. The translator buffers args per block and at
// content_block_stop emits either the valid payload, a minimal deterministic
// repair, or `{}` as last resort — turning a stream-parser-fatal turn (CC's
// strict parser rejects malformed input_json_delta) into a dispatchable call
// that errors cleanly and triggers a normal CC retry.

// driveAnthropicSSEWithSummary feeds events through a translator, returning
// the translated body and Summary for assertions.
func driveAnthropicSSEWithSummary(
	t *testing.T,
	events []string,
) (string, translate.ResponseSummary) {
	t.Helper()
	rec := httptest.NewRecorder()
	translator := translate.NewAnthropicSSETranslator(rec, "z-ai/glm-5.1", nil)
	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)
	for _, e := range events {
		_, err := translator.Write([]byte(e))
		require.NoError(t, err)
	}
	require.NoError(t, translator.Finalize())
	return rec.Body.String(), translator.Summary()
}

func TestAnthropicSSETranslator_RepairsTruncatedToolArgs(t *testing.T) {
	// Truncated JSON (brace/key/colon/string then EOF) is the common GLM/Kimi
	// max_tokens-cutoff shape; repair closes it, preserving intact args instead of `{}`.
	body, summary := driveAnthropicSSEWithSummary(t, []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"Edit","arguments":"{\"path\":\"a.go\",\"old_string\":\"hel"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n",
		"data: [DONE]\n\n",
	})

	// Tool_use block still emits so stop_reason="tool_use" is honored.
	assert.Contains(t, body, `"type":"tool_use"`)
	assert.Contains(t, body, `"name":"Edit"`)
	assert.Equal(t, 0, summary.InvalidToolArgsBlocks,
		"a repaired payload is dispatchable — the unrecoverable-args latch must not fire")
	require.Len(t, summary.ToolCallIssues, 1,
		"the repair must still be reported so the proxy logs the malformed turn")
	assert.Equal(t, "invalid_json", string(summary.ToolCallIssues[0].Bucket))
	assert.True(t, summary.ToolCallIssues[0].Repaired)

	// The raw malformed bytes must not reach the client — only the closed,
	// valid repair does (one consolidated input_json_delta).
	assert.Contains(t, body, `"partial_json":"{\"path\":\"a.go\",\"old_string\":\"hel\"}"`,
		"repair closes the truncated string/brace and keeps the intact args")
}

func TestAnthropicSSETranslator_SubstitutesEmptyArgsWhenUnrepairable(t *testing.T) {
	// Mismatched closer can't be minimally repaired, so the translator falls
	// back to `{}`; CC's parser accepts it, the tool errors on missing params,
	// and CC retries normally instead of dying mid-stream.
	body, summary := driveAnthropicSSEWithSummary(t, []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"Edit","arguments":"{\"path\":\"a.go\"]"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n",
		"data: [DONE]\n\n",
	})

	assert.Contains(t, body, `"type":"tool_use"`)
	assert.Equal(t, 1, summary.InvalidToolArgsBlocks,
		"unrepairable args must be flagged in Summary so the proxy can log the malformed turn")
	assert.NotContains(t, body, `\"path\":\"a.go\"]`,
		"the malformed args fragment must not reach the client")
	assert.Contains(t, body, `"partial_json":"{}"`,
		"unrepairable args must be substituted with empty `{}` payload so CC's parser succeeds")
}

func TestAnthropicSSETranslator_AcceptsValidToolArgs(t *testing.T) {
	body, summary := driveAnthropicSSEWithSummary(t, []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"Edit","arguments":"{\"path\":\"a.go\","}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"old_string\":\"x\",\"new_string\":\"y\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		"data: [DONE]\n\n",
	})

	assert.Equal(t, 0, summary.InvalidToolArgsBlocks,
		"well-formed JSON args concatenated across fragments must not be flagged")
	assert.Equal(t, 1, summary.ToolUseBlocks)
	// Valid args flow through verbatim — concatenated into a single
	// input_json_delta at content_block_stop time.
	assert.Contains(t, body, `"partial_json":"{\"path\":\"a.go\",\"old_string\":\"x\",\"new_string\":\"y\"}"`,
		"valid buffered args must reach the client unchanged, as one delta")
}

func TestAnthropicSSETranslator_FlagsEachInvalidBlockIndependently(t *testing.T) {
	// Two tool_calls, one valid and one unrepairable. Each block is
	// buffered/validated under its own content-block index, so the count tracks blocks not turns.
	_, summary := driveAnthropicSSEWithSummary(t, []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"Read","arguments":"{\"path\":\"a.go\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"Edit","arguments":"{\"path\":\"b.go\"]"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		"data: [DONE]\n\n",
	})

	assert.Equal(t, 2, summary.ToolUseBlocks)
	assert.Equal(t, 1, summary.InvalidToolArgsBlocks,
		"only the malformed block must be flagged; the valid sibling is unaffected")
	require.Len(t, summary.ToolCallIssues, 1)
	assert.Equal(t, "Edit", summary.ToolCallIssues[0].ToolName)
}

// driveAnthropicSSEWithTools is driveAnthropicSSEWithSummary plus a "had
// tools" flag, for exercising the nudge-synthesis path.
func driveAnthropicSSEWithTools(
	t *testing.T,
	model string,
	hadTools bool,
	events []string,
) (string, translate.ResponseSummary) {
	t.Helper()
	rec := httptest.NewRecorder()
	translator := translate.NewAnthropicSSETranslator(rec, model, nil)
	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)
	for _, e := range events {
		_, err := translator.Write([]byte(e))
		require.NoError(t, err)
	}
	require.NoError(t, translator.Finalize())
	return rec.Body.String(), translator.Summary()
}

func TestAnthropicSSETranslator_TextOnlyTurnPassesThroughWithoutSyntheticToolUse(t *testing.T) {
	body, summary := driveAnthropicSSEWithTools(t, "mimo-v2.5-pro", true, []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"<tool_call>{\"name\":\"Read\",\"path\":\"a.go\"}</tool_call>"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":15}}` + "\n\n",
		"data: [DONE]\n\n",
	})

	assert.Equal(t, 0, summary.ToolUseBlocks, "the router never fabricates a tool_use")
	assert.Equal(t, "end_turn", summary.StopReason)
	assert.NotContains(t, body, `"name":"Bash"`)
}

func TestAnthropicSSETranslator_EmptyArgsNotFlagged(t *testing.T) {
	// Some tools take no input, so an empty/missing arguments buffer is the
	// trivial case, not malformed.
	_, summary := driveAnthropicSSEWithSummary(t, []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"Ping"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		"data: [DONE]\n\n",
	})

	assert.Equal(t, 0, summary.InvalidToolArgsBlocks,
		"a tool with no streamed arguments is not the malformed-args case")
}
