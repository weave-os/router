package translate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate/toolcheck"
)

func acceptanceFrames(wire string) []gjson.Result {
	var frames []gjson.Result
	for _, line := range strings.Split(wire, "\n") {
		if strings.HasPrefix(line, "data: {") {
			frames = append(frames, gjson.Parse(strings.TrimPrefix(line, "data: ")))
		}
	}
	return frames
}

func TestToolCallLedger_ProjectedIdentityCollisions(t *testing.T) {
	for name, project := range map[string]func(string) string{
		"anthropic": sanitizeToolUseID,
		"chat":      clampOpenAIToolCallID,
	} {
		t.Run(name, func(t *testing.T) {
			ledger := NewToolCallLedger()
			ledger.Upsert(0, "call.a", "Read")
			ledger.Upsert(1, "call_a", "Write")
			first := ledger.ProjectID(0, project)
			second := ledger.ProjectID(1, project)
			assert.NotEmpty(t, first)
			assert.NotEmpty(t, second)
			assert.NotEqual(t, first, second)
			ledger.Upsert(0, "late", "Read")
			assert.Equal(t, first, ledger.ProjectID(0, project))
		})
	}
	// OpenAI strips Gemini signature carriers, which can also create collisions.
	ledger := NewToolCallLedger()
	ledger.Upsert(0, embedSignatureInID("same", "sig_one"), "Read")
	ledger.Upsert(1, embedSignatureInID("same", "sig_two"), "Write")
	assert.NotEqual(t, ledger.ProjectID(0, clampOpenAIToolCallID), ledger.ProjectID(1, clampOpenAIToolCallID))
}

func TestResponsesAcceptance_SelectionAndDuplicateClosure(t *testing.T) {
	for _, fragment := range []string{`{"path":"stream.txt"}`, "invalid", ""} {
		for _, anthropic := range []bool{false, true} {
			t.Run(fmt.Sprintf("fragment=%s/anthropic=%v", fragment, anthropic), func(t *testing.T) {
				item := json.RawMessage(`{"type":"function_call","call_id":"late","name":"Read","arguments":"{\"path\":\"terminal.txt\",\"pages\":null}"}`)
				stream := acceptanceEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call"}})
				stream += acceptanceEvent("response.function_call_arguments.delta", map[string]any{"output_index": 0, "delta": fragment})
				done := acceptanceEvent("response.output_item.done", map[string]any{"output_index": 0, "item": item})
				stream += done + done
				stream += acceptanceEvent("response.completed", map[string]any{"response": json.RawMessage(acceptanceResponse("[" + string(item) + "]"))})
				rec := httptest.NewRecorder()
				validator := toolcheck.Compile([]byte(`[{"name":"Read","input_schema":{"type":"object","properties":{"path":{"type":"string"},"pages":{"type":"string"}},"required":["path"]}}]`))
				var summary ResponseSummary
				if anthropic {
					writer := NewResponsesToAnthropicWriter(rec, "synthetic", nil).WithToolValidator(validator)
					require.NoError(t, writer.Prelude(true))
					_, err := writer.Write([]byte(stream))
					require.NoError(t, err)
					require.NoError(t, writer.Finalize())
					summary = writer.Summary()
				} else {
					writer := NewResponsesToOpenAIChatWriter(rec, "synthetic", nil).WithToolValidator(validator)
					require.NoError(t, writer.Prelude(true))
					_, err := writer.Write([]byte(stream))
					require.NoError(t, err)
					require.NoError(t, writer.Finalize())
					summary = writer.Summary()
				}
				assert.Equal(t, 1, summary.ToolUseBlocks)
				assert.Empty(t, summary.ToolCallIssues)
				var arguments []string
				for _, frame := range acceptanceFrames(rec.Body.String()) {
					if args := frame.Get("choices.0.delta.tool_calls.0.function.arguments"); args.Exists() {
						arguments = append(arguments, args.String())
					}
					if frame.Get("delta.type").String() == "input_json_delta" {
						arguments = append(arguments, frame.Get("delta.partial_json").String())
					}
				}
				require.Len(t, arguments, 1)
				want := `{"path":"terminal.txt"}`
				if gjson.Valid(fragment) {
					want = fragment
				}
				assert.JSONEq(t, want, arguments[0])
			})
		}
	}
}

func TestChatAcceptance_LateNameStreamingAdapters(t *testing.T) {
	stream := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":7,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}` + "\n\n"
	stream += "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":7,"id":"late","function":{"name":"Read","arguments":"\"synthetic.txt\"}"}}]},"finish_reason":null}]}` + "\n\n"
	stream += "data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	for _, responses := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%v", responses), func(t *testing.T) {
			rec := httptest.NewRecorder()
			if responses {
				writer := NewResponsesWriter(rec, "synthetic")
				require.NoError(t, writer.Prelude(true))
				_, err := writer.Write([]byte(stream))
				require.NoError(t, err)
				require.NoError(t, writer.Finalize())
			} else {
				writer := NewAnthropicSSETranslator(rec, "synthetic", nil)
				writer.Header().Set("Content-Type", "text/event-stream")
				writer.WriteHeader(http.StatusOK)
				_, err := writer.Write([]byte(stream))
				require.NoError(t, err)
				require.NoError(t, writer.Finalize())
			}
			var ids, arguments []string
			for _, frame := range acceptanceFrames(rec.Body.String()) {
				if frame.Get("content_block.type").String() == "tool_use" {
					ids = append(ids, frame.Get("content_block.id").String())
				}
				if frame.Get("type").String() == "response.output_item.added" && frame.Get("item.type").String() == "function_call" {
					ids = append(ids, frame.Get("item.call_id").String())
				}
				if frame.Get("delta.type").String() == "input_json_delta" {
					arguments = append(arguments, frame.Get("delta.partial_json").String())
				}
				if frame.Get("type").String() == "response.function_call_arguments.done" {
					arguments = append(arguments, frame.Get("arguments").String())
				}
			}
			require.Len(t, ids, 1)
			assert.True(t, strings.HasPrefix(ids[0], "call_router_1"))
			require.Len(t, arguments, 1)
			assert.JSONEq(t, `{"path":"synthetic.txt"}`, arguments[0])
		})
	}
}

func TestAnthropicAcceptance_InitialInputDoesNotMaskMalformedDelta(t *testing.T) {
	for _, omitBlockStop := range []bool{false, true} {
		t.Run(fmt.Sprintf("omit_block_stop=%v", omitBlockStop), func(t *testing.T) {
			rec := httptest.NewRecorder()
			writer := NewSSETranslator(rec, "synthetic", nil)
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			event := func(kind string, fields map[string]any) string {
				return "event: " + kind + "\n" + acceptanceEvent(kind, fields)
			}
			stream := event("message_start", map[string]any{"message": map[string]any{"id": "msg_synthetic"}})
			stream += event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{}}})
			stream += event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"path":"synthetic.txt"`}})
			if !omitBlockStop {
				stream += event("content_block_stop", map[string]any{"index": 0})
			}
			stream += event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}})
			stream += event("message_stop", map[string]any{})
			_, err := writer.Write([]byte(stream))
			require.NoError(t, err)
			require.NoError(t, writer.Finalize())
			var arguments string
			for _, frame := range acceptanceFrames(rec.Body.String()) {
				arguments += frame.Get("choices.0.delta.tool_calls.0.function.arguments").String()
			}
			assert.JSONEq(t, `{"path":"synthetic.txt"}`, arguments)
		})
	}
}

func TestResponsesAcceptance_RepairIssueCollectedOnce(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := NewResponsesToOpenAIChatWriter(rec, "synthetic", nil)
	require.NoError(t, writer.Prelude(true))
	item := json.RawMessage(`{"type":"function_call","name":"Read","arguments":"{\"path\":\"synthetic.txt\""}`)
	done := acceptanceEvent("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	stream := done + done + acceptanceEvent("response.completed", map[string]any{"response": json.RawMessage(acceptanceResponse("[" + string(item) + "]"))})
	_, err := writer.Write([]byte(stream))
	require.NoError(t, err)
	require.NoError(t, writer.Finalize())
	summary := writer.Summary()
	assert.Equal(t, 1, summary.ToolUseBlocks)
	require.Len(t, summary.ToolCallIssues, 1)
	assert.Equal(t, toolcheck.BucketInvalidJSON, summary.ToolCallIssues[0].Bucket)
	assert.True(t, summary.ToolCallIssues[0].Repaired)
}

func TestResponsesAcceptance_SuppressionPreservesReasoningCarrier(t *testing.T) {
	response := acceptanceResponse(`[{"type":"reasoning","id":"rs_synthetic","encrypted_content":"opaque","summary":[]},{"type":"function_call","call_id":"nameless","name":"","arguments":"{}"},{"type":"function_call","call_id":"call.a","name":"Read","arguments":"{}"},{"type":"function_call","call_id":"call_a","name":"Write","arguments":"{}"}]`)
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			rec := httptest.NewRecorder()
			writer := NewResponsesToAnthropicWriter(rec, "synthetic", nil).WithReasoningScope("synthetic_scope")
			require.NoError(t, writer.Prelude(streaming))
			_, err := writer.Write([]byte(acceptanceEvent("response.completed", map[string]any{"response": json.RawMessage(response)})))
			require.NoError(t, err)
			require.NoError(t, writer.Finalize())
			var ids []string
			if streaming {
				for _, frame := range acceptanceFrames(rec.Body.String()) {
					if frame.Get("content_block.type").String() == "tool_use" {
						ids = append(ids, frame.Get("content_block.id").String())
					}
				}
			} else {
				for _, block := range gjson.Get(rec.Body.String(), "content").Array() {
					if block.Get("type").String() == "tool_use" {
						ids = append(ids, block.Get("id").String())
					}
				}
			}
			require.Len(t, ids, 2)
			first, signature := extractOpenAIReasoningSignatureFromID(ids[0])
			assert.Equal(t, "call_a", first)
			assert.NotEqual(t, first, ids[1])
			reasoningID, encrypted, ok := openAIReasoningSignatureForScope(signature, "synthetic_scope")
			require.True(t, ok)
			assert.Equal(t, "rs_synthetic", reasoningID)
			assert.Equal(t, "opaque", encrypted)
		})
	}
}

func TestResponsesAcceptance_ResetPendingAttempt(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := NewResponsesWriter(rec, "synthetic")
	require.NoError(t, writer.Prelude(true))
	_, err := writer.Write([]byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"invalid"}}]}}]}` + "\n\n"))
	require.NoError(t, err)
	assert.NotContains(t, rec.Body.String(), `"function_call"`)
	writer.ResetAttempt()
	stream := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"Read","arguments":"{\"path\":\"synthetic.txt\"}"}}]}}]}` + "\n\n"
	stream += "data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	_, err = writer.Write([]byte(stream))
	require.NoError(t, err)
	require.NoError(t, writer.Finalize())
	var output gjson.Result
	for _, frame := range acceptanceFrames(rec.Body.String()) {
		if frame.Get("type").String() == "response.completed" {
			output = frame.Get("response.output")
		}
	}
	require.Len(t, output.Array(), 1)
	assert.Equal(t, "call_router_1", output.Get("0.call_id").String())
	assert.JSONEq(t, `{"path":"synthetic.txt"}`, output.Get("0.arguments").String())
}
