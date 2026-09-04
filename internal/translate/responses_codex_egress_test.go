package translate_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesWriter_RestoresCodexCustomToolFromSplitChatArguments(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetToolMappings(map[string]translate.ResponsesToolMapping{
		"ww_custom_exec": {Alias: "ww_custom_exec", Name: "exec", Custom: true},
	})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-router-model", "moonshotai/kimi-k2.7")
	w.WriteHeader(200)

	for _, chunk := range []string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"ww_custom_exec","arguments":"{\"input\":\"const x = "}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1;\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		_, err := w.Write([]byte(chunk))
		require.NoError(t, err)
	}
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	var added, delta, done, completed map[string]any
	for _, event := range events {
		switch event["type"] {
		case "response.output_item.added":
			item, _ := event["item"].(map[string]any)
			if item["type"] == "custom_tool_call" {
				added = event
			}
		case "response.custom_tool_call_input.delta":
			delta = event
		case "response.output_item.done":
			item, _ := event["item"].(map[string]any)
			if item["type"] == "custom_tool_call" {
				done = event
			}
		case "response.completed":
			completed = event
		}
		assert.NotEqual(t, "response.function_call_arguments.delta", event["type"])
		assert.NotEqual(t, "response.function_call_arguments.done", event["type"])
	}
	require.NotNil(t, added)
	require.NotNil(t, delta)
	require.NotNil(t, done)
	require.NotNil(t, completed)
	assert.Equal(t, "const x = 1;", delta["delta"])
	item := done["item"].(map[string]any)
	assert.Equal(t, "exec", item["name"])
	assert.Equal(t, "call_exec", item["call_id"])
	assert.Equal(t, "const x = 1;", item["input"])
	// ctc_ required for custom_tool_call; Codex replays this id next turn.
	assert.Truef(t, strings.HasPrefix(item["id"].(string), "ctc_"), "custom_tool_call id %q must start with ctc_", item["id"])
	output := completed["response"].(map[string]any)["output"].([]any)
	assert.Equal(t, "custom_tool_call", output[0].(map[string]any)["type"])
	assert.Equal(t, "const x = 1;", output[0].(map[string]any)["input"])
	assert.Truef(t, strings.HasPrefix(output[0].(map[string]any)["id"].(string), "ctc_"), "response.completed custom_tool_call id %q must start with ctc_", output[0].(map[string]any)["id"])
}

func TestResponsesWriter_RestoresCodexFunctionNamespace(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetToolMappings(map[string]translate.ResponsesToolMapping{
		"ww_fn_collaboration_send_message": {
			Alias: "ww_fn_collaboration_send_message", Name: "send_message", Namespace: "collaboration",
		},
	})
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, err := w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_send","type":"function","function":{"name":"ww_fn_collaboration_send_message","arguments":"{\"target\":\"/root\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	for _, event := range events {
		if event["type"] != "response.output_item.done" {
			continue
		}
		item := event["item"].(map[string]any)
		if item["type"] != "function_call" {
			continue
		}
		assert.Equal(t, "send_message", item["name"])
		assert.Equal(t, "collaboration", item["namespace"])
		assert.JSONEq(t, `{"target":"/root"}`, item["arguments"].(string))
		assert.Truef(t, strings.HasPrefix(item["id"].(string), "fc_"), "function_call id %q must start with fc_", item["id"])
		return
	}
	t.Fatal("missing namespaced function_call output item")
}

func TestResponsesWriter_NonStreamingRestoresCodexCustomTool(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetToolMappings(map[string]translate.ResponsesToolMapping{
		"ww_custom_exec": {Alias: "ww_custom_exec", Name: "exec", Custom: true},
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	body := map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id": "call_exec", "type": "function",
				"function": map[string]any{"name": "ww_custom_exec", "arguments": `{"input":"return 1;"}`},
			}},
		}}},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = w.Write(raw)
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	output := response["output"].([]any)
	assert.Equal(t, "custom_tool_call", output[0].(map[string]any)["type"])
	assert.Equal(t, "exec", output[0].(map[string]any)["name"])
	assert.Equal(t, "return 1;", output[0].(map[string]any)["input"])
	assert.Truef(t, strings.HasPrefix(output[0].(map[string]any)["id"].(string), "ctc_"), "non-streaming custom_tool_call id %q must start with ctc_", output[0].(map[string]any)["id"])
}

func TestResponsesWriter_RejectsMalformedCodexCustomWrapperAndTerminatesFailed(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetToolMappings(map[string]translate.ResponsesToolMapping{
		"exec": {Alias: "exec", Name: "exec", Custom: true},
	})
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)

	_, err := w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"exec","arguments":"{\"input\":\"ok\",\"extra\":true}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
	require.ErrorContains(t, err, "expected exactly one input field")
	require.Error(t, w.FinalizeError(err))

	events := parseSSEEvents(t, rec.Body.Bytes())
	var failed bool
	for _, event := range events {
		assert.NotEqual(t, "response.completed", event["type"])
		if event["type"] == "response.failed" {
			failed = true
		}
	}
	assert.True(t, failed, "a malformed custom call must terminate the committed Codex stream")
}

func TestResponsesWriter_PortableCodexBuffersUntilLateCustomToolName(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetToolMappings(map[string]translate.ResponsesToolMapping{
		"exec": {Alias: "exec", Name: "exec", Custom: true},
	})
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)

	for _, chunk := range []string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"arguments":"{\"input\":\"return "}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"exec","arguments":"1;\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
	} {
		_, err := w.Write([]byte(chunk))
		require.NoError(t, err)
	}
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	var customAdded, customDone bool
	for _, event := range events {
		if event["type"] != "response.output_item.added" && event["type"] != "response.output_item.done" {
			continue
		}
		item := event["item"].(map[string]any)
		assert.NotEmpty(t, item["name"])
		assert.Equal(t, "custom_tool_call", item["type"])
		// id must be ctc_ even though name (and thus mapping.Custom) arrives late.
		assert.Truef(t, strings.HasPrefix(item["id"].(string), "ctc_"), "late-named custom_tool_call id %q must start with ctc_", item["id"])
		customAdded = customAdded || event["type"] == "response.output_item.added"
		customDone = customDone || event["type"] == "response.output_item.done"
		if event["type"] == "response.output_item.done" {
			assert.Equal(t, "return 1;", item["input"])
		}
	}
	assert.True(t, customAdded)
	assert.True(t, customDone)
}

func TestResponsesWriter_PortableCodexRejectsNamelessToolCall(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetToolMappings(map[string]translate.ResponsesToolMapping{
		"exec": {Alias: "exec", Name: "exec", Custom: true},
	})
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)

	_, err := w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_bad","type":"function","function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
	require.ErrorContains(t, err, "missing a function name")
	require.Error(t, w.FinalizeError(err))
	assert.Contains(t, rec.Body.String(), `"type":"response.failed"`)
	assert.NotContains(t, rec.Body.String(), `"type":"function_call"`)
	assert.NotContains(t, rec.Body.String(), `"type":"custom_tool_call"`)
}

func TestResponsesWriter_WithoutCodexMappingsPreservesLegacyFunctionShape(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, err := w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	for _, event := range events {
		if event["type"] != "response.output_item.added" && event["type"] != "response.output_item.done" {
			continue
		}
		item, ok := event["item"].(map[string]any)
		if !ok || item["type"] != "function_call" {
			continue
		}
		_, hasNamespace := item["namespace"]
		assert.False(t, hasNamespace, "non-Codex translated Responses output must retain its legacy shape")
	}
}
