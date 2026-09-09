package translate_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/translate"
)

func TestEscalationResponsesCompletionReconstructsContinuation(t *testing.T) {
	terminal := `{"id":"resp-2","status":"completed","output":[{"type":"reasoning","encrypted_content":"hidden"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"checking"}]},{"type":"function_call","call_id":"call-2","name":"run","arguments":"{\"command\":\"go test\"}"}]}`
	for _, body := range []string{terminal, "event: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":" + terminal + "}\r\n\r\n"} {
		response, err := translate.ParseEscalationResponse([]byte(body), translate.EscalationResponseResponses)
		require.NoError(t, err)
		assert.Equal(t, "resp-2", response.ResponseID)
		require.Len(t, response.Messages, 2)
		assert.Equal(t, "checking", response.Messages[0].Blocks[0].Text)
		call := response.Messages[1].Blocks[0]
		assert.Equal(t, "call-2", call.ID)
		assert.Equal(t, "run", call.Name)
		assert.JSONEq(t, `{"command":"go test"}`, call.ArgumentsJSON)
	}
}

func TestEscalationResponseStreamAssembly(t *testing.T) {
	fixtures := []struct {
		name   string
		format translate.EscalationResponseFormat
		body   string
	}{
		{"anthropic", translate.EscalationResponseAnthropic, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"resp-1\"}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"checking\"}}\n\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-1\",\"name\":\"run\",\"input\":{}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"x\\\":\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"1}\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"chat", translate.EscalationResponseChat, "data: {\"id\":\"resp-1\",\"choices\":[{\"delta\":{\"content\":\"checking\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"run\",\"arguments\":\"{\\\"x\\\":\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"},
		{"gemini", translate.EscalationResponseGemini, "data: {\"responseId\":\"resp-1\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"checking\"}]}}]}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"call-1\",\"name\":\"run\",\"args\":{\"x\":1}}}]},\"finishReason\":\"STOP\"}]}\n\n"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			response, err := translate.ParseEscalationResponse([]byte(fixture.body), fixture.format)
			require.NoError(t, err)
			assert.Equal(t, "resp-1", response.ResponseID)
			require.Len(t, response.Messages, 1)
			require.Len(t, response.Messages[0].Blocks, 2)
			assert.Equal(t, "checking", response.Messages[0].Blocks[0].Text)
			call := response.Messages[0].Blocks[1]
			assert.Equal(t, "call-1", call.ID)
			assert.Equal(t, "run", call.Name)
			assert.JSONEq(t, `{"x":1}`, call.ArgumentsJSON)
		})
	}
}

func TestEscalationResponseJSONAcrossProtocols(t *testing.T) {
	fixtures := []struct {
		format translate.EscalationResponseFormat
		body   string
	}{
		{translate.EscalationResponseAnthropic, `{"id":"response","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`},
		{translate.EscalationResponseChat, `{"id":"response","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`},
		{translate.EscalationResponseGemini, `{"responseId":"response","candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`},
	}
	for _, fixture := range fixtures {
		response, err := translate.ParseEscalationResponse([]byte(fixture.body), fixture.format)
		require.NoError(t, err)
		assert.Equal(t, "response", response.ResponseID)
		assert.Equal(t, "done", response.Messages[0].Blocks[0].Text)
	}
}

func TestEscalationResponseRejectsIncompleteOrFailedStreams(t *testing.T) {
	fixtures := []struct {
		format translate.EscalationResponseFormat
		body   string
	}{
		{translate.EscalationResponseResponses, "data: {\"type\":\"response.created\"}\n\n"},
		{translate.EscalationResponseResponses, "data: {\"type\":\"response.failed\"}\n\n"},
		{translate.EscalationResponseAnthropic, "data: {\"type\":\"message_start\"}\n\n"},
		{translate.EscalationResponseChat, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"},
		{translate.EscalationResponseGemini, `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`},
		{translate.EscalationResponseChat, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run","arguments":"{"}}]},"finish_reason":"length"}]}`},
	}
	for _, fixture := range fixtures {
		_, err := translate.ParseEscalationResponse([]byte(fixture.body), fixture.format)
		assert.Error(t, err)
	}
}

func TestEscalationResponseRejectsUnparsedSSETails(t *testing.T) {
	terminal := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-1\",\"status\":\"completed\",\"output\":[]}}"
	for _, body := range []string{
		terminal,
		terminal + "\n",
		terminal + "\n\ndata: {\"partial\":",
		terminal + "\n\ndata: invalid\n\n",
		terminal + "\ndata: {\"unexpected\":true}\n\n",
	} {
		_, err := translate.ParseEscalationResponse([]byte(body), translate.EscalationResponseResponses)
		assert.Error(t, err)
	}
	response, err := translate.ParseEscalationResponse([]byte(terminal+"\n\n: keepalive\n\n"), translate.EscalationResponseResponses)
	require.NoError(t, err)
	assert.Equal(t, "response-1", response.ResponseID)
}

func TestEscalationResponseRequiresSuccessfulTerminalReason(t *testing.T) {
	protocols := []struct {
		format     translate.EscalationResponseFormat
		successful []string
		incomplete []string
	}{
		{translate.EscalationResponseAnthropic, []string{"end_turn", "tool_use", "stop_sequence"}, []string{"max_tokens", "pause_turn", "refusal", "unknown", ""}},
		{translate.EscalationResponseChat, []string{"stop", "tool_calls", "function_call"}, []string{"length", "content_filter", "unknown", ""}},
		{translate.EscalationResponseGemini, []string{"STOP"}, []string{"MAX_TOKENS", "SAFETY", "RECITATION", "MALFORMED_FUNCTION_CALL", "OTHER", "unknown", ""}},
	}
	for _, protocol := range protocols {
		for _, streaming := range []bool{false, true} {
			check := func(reasonJSON string, complete bool) {
				t.Helper()
				t.Run(fmt.Sprintf("%s/stream=%t/reason=%s", protocol.format, streaming, reasonJSON), func(t *testing.T) {
					body := escalationTerminalFixture(protocol.format, streaming, reasonJSON)
					response, err := translate.ParseEscalationResponse([]byte(body), protocol.format)
					if complete {
						require.NoError(t, err)
						require.Len(t, response.Messages, 1)
						assert.Equal(t, "evidence", response.Messages[0].Blocks[0].Text)
					} else {
						require.Error(t, err)
						assert.Empty(t, response.Messages)
					}
				})
			}
			for _, reason := range protocol.successful {
				encoded, err := json.Marshal(reason)
				require.NoError(t, err)
				check(string(encoded), true)
			}
			for _, reason := range protocol.incomplete {
				encoded, err := json.Marshal(reason)
				require.NoError(t, err)
				check(string(encoded), false)
			}
			check("null", false)
			check("", false) // Omitted reason must also fail open.
		}
	}
}

func escalationTerminalFixture(format translate.EscalationResponseFormat, streaming bool, reasonJSON string) string {
	reasonField := func(name string) string {
		if reasonJSON == "" {
			return ""
		}
		return fmt.Sprintf(",%q:%s", name, reasonJSON)
	}
	sseEvent := func(payload string) string { return "data: " + payload + "\n\n" }
	switch format {
	case translate.EscalationResponseAnthropic:
		if streaming {
			return sseEvent(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"evidence"}}`) +
				sseEvent(`{"type":"message_delta","delta":{"type":"message_delta"`+reasonField("stop_reason")+`}}`) +
				sseEvent(`{"type":"message_stop"}`)
		}
		return `{"content":[{"type":"text","text":"evidence"}]` + reasonField("stop_reason") + `}`
	case translate.EscalationResponseChat:
		messageKey := "message"
		if streaming {
			messageKey = "delta"
		}
		legacyFunction := ""
		if reasonJSON == `"function_call"` {
			legacyFunction = `,"function_call":{"name":"run","arguments":"{}"}`
		}
		body := fmt.Sprintf(`{"choices":[{%q:{"content":"evidence"%s}%s}]}`, messageKey, legacyFunction, reasonField("finish_reason"))
		if streaming {
			return sseEvent(body) + sseEvent("[DONE]")
		}
		return body
	case translate.EscalationResponseGemini:
		body := `{"candidates":[{"content":{"parts":[{"text":"evidence"}]}` + reasonField("finishReason") + `}]}`
		if streaming {
			return sseEvent(body)
		}
		return body
	default:
		panic("unsupported terminal fixture protocol")
	}
}

func TestEscalationResponseRequiresStreamEndAfterSuccessfulReason(t *testing.T) {
	for _, fixture := range []struct {
		format translate.EscalationResponseFormat
		body   string
	}{
		{translate.EscalationResponseAnthropic, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"},
		{translate.EscalationResponseChat, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"stop\"}]}\n\n"},
	} {
		_, err := translate.ParseEscalationResponse([]byte(fixture.body), fixture.format)
		assert.Error(t, err)
	}
}

func TestEscalationChatLegacyFunctionCallAssembly(t *testing.T) {
	for _, body := range []string{
		`{"id":"legacy-response","choices":[{"message":{"role":"assistant","content":"checking","function_call":{"name":"run","arguments":"{\"command\":\"go test\"}"}},"finish_reason":"function_call"}]}`,
		"data: {\"id\":\"legacy-response\",\"choices\":[{\"delta\":{\"content\":\"checking\",\"function_call\":{\"name\":\"r\",\"arguments\":\"\"}},\"finish_reason\":null}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"function_call\":{\"name\":\"un\",\"arguments\":\"{\\\"command\\\":\"}},\"finish_reason\":null}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"function_call\":{\"arguments\":\"\\\"go test\\\"}\"}},\"finish_reason\":null}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"function_call\"}]}\n\ndata: [DONE]\n\n",
	} {
		response, err := translate.ParseEscalationResponse([]byte(body), translate.EscalationResponseChat)
		require.NoError(t, err)
		assert.Equal(t, "legacy-response", response.ResponseID)
		require.Len(t, response.Messages, 1)
		require.Len(t, response.Messages[0].Blocks, 2)
		assert.Equal(t, "checking", response.Messages[0].Blocks[0].Text)
		call := response.Messages[0].Blocks[1]
		assert.Equal(t, translate.EscalationBlockToolCall, call.Type)
		assert.Equal(t, "run", call.Name)
		assert.Empty(t, call.ID)
		assert.JSONEq(t, `{"command":"go test"}`, call.ArgumentsJSON)
	}
}

func TestEscalationChatRejectsIncompleteLegacyFunctionCalls(t *testing.T) {
	for _, message := range []string{
		`{"content":"partial"}`,
		`{"function_call":null}`,
		`{"function_call":{}}`,
		`{"function_call":{"name":"run","arguments":"{"}}`,
		`{"function_call":{"name":"run"}}`,
		`{"function_call":{"arguments":"{}"}}`,
		`{"function_call":{"name":" ","arguments":"{}"}}`,
		`{"function_call":{"name":123,"arguments":"{}"}}`,
		`{"function_call":{"name":"run","arguments":{}}}`,
		`{"function_call":{"name":"run","arguments":"{}"},"tool_calls":[{"id":"call-1","function":{"name":"run","arguments":"{}"}}]}`,
	} {
		for _, streaming := range []bool{false, true} {
			messageKey := "message"
			if streaming {
				messageKey = "delta"
			}
			body := fmt.Sprintf(`{"choices":[{%q:%s,"finish_reason":"function_call"}]}`, messageKey, message)
			if streaming {
				body = "data: " + body + "\n\ndata: [DONE]\n\n"
			}
			response, err := translate.ParseEscalationResponse([]byte(body), translate.EscalationResponseChat)
			require.Error(t, err, "stream=%t message=%s", streaming, message)
			assert.Empty(t, response.Messages)
		}
	}
}
