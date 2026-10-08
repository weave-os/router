package translate_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesWriterDeclaresTranslatedFunctionArgumentsPlaintext(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "buffered"}[stream], func(t *testing.T) {
			sink := httptest.NewRecorder()
			writer := translate.NewResponsesWriter(sink, "gpt-6.1-sol")
			writer.SetToolMappings(map[string]translate.ResponsesToolMapping{
				"collaboration__spawn_agent": {Alias: "collaboration__spawn_agent", Name: "spawn_agent", Namespace: "collaboration"},
			})
			if stream {
				writer.Header().Set("Content-Type", "text/event-stream")
				writer.WriteHeader(200)
				_, err := writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_task\",\"function\":{\"name\":\"collaboration__spawn_agent\",\"arguments\":\"{\\\"message\\\":\\\"Inspect project tests\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
				require.NoError(t, err)
			} else {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(200)
				_, err := writer.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_task","function":{"name":"collaboration__spawn_agent","arguments":"{\"message\":\"Inspect project tests\"}"}}]},"finish_reason":"tool_calls"}]}`))
				require.NoError(t, err)
			}
			require.NoError(t, writer.Finalize())
			var calls []gjson.Result
			if stream {
				for _, event := range parseSSEEvents(t, sink.Body.Bytes()) {
					encoded, err := json.Marshal(event)
					require.NoError(t, err)
					switch event["type"] {
					case "response.output_item.added", "response.output_item.done":
						calls = append(calls, gjson.GetBytes(encoded, "item"))
					case "response.completed":
						calls = append(calls, gjson.GetBytes(encoded, "response.output.0"))
					}
				}
				require.Len(t, calls, 3, "Codex consumes done.item; all copies must agree")
			} else {
				calls = append(calls, gjson.GetBytes(sink.Body.Bytes(), "output.0"))
			}
			for _, call := range calls {
				assert.Equal(t, "spawn_agent", call.Get("name").Str)
				assert.Equal(t, "collaboration", call.Get("namespace").Str)
				marker := call.Get("encrypted_function_args")
				require.True(t, marker.IsArray(), "absent/null selects encrypted task delivery in Codex")
				assert.Empty(t, marker.Array())
			}
			assert.JSONEq(t, `{"message":"Inspect project tests"}`, calls[len(calls)-1].Get("arguments").Str)
		})
	}
}

func TestPortableCodexAgentDeliveryPreservesTask(t *testing.T) {
	body := []byte(`{"input":[{"type":"agent_message","author":"parent","recipient":"child","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: child\nSender: parent\nPayload:\nInspect project tests"}]}]}`)
	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.False(t, converted.Requirements.NativeOnly)
	assert.Equal(t, string(translate.EscalationRoleUser), gjson.GetBytes(converted.Body, "messages.0.role").Str)
	assert.Equal(t, "Message Type: NEW_TASK\nTask name: child\nSender: parent\nPayload:\nInspect project tests", gjson.GetBytes(converted.Body, "messages.0.content").Str)
	assert.Equal(t, body, converted.OriginalBody)
	observation, err := translate.ParseResponsesEscalationObservation(body)
	require.NoError(t, err)
	require.Len(t, observation.Messages, 1)
	assert.Equal(t, translate.EscalationRoleUser, observation.Messages[0].Role)
}

func TestResponsesWriterPreservesNativeEncryptedCollaboration(t *testing.T) {
	sink := httptest.NewRecorder()
	writer := translate.NewResponsesWriter(sink, "gpt-6.1-sol")
	writer.SetPassthrough()
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(200)
	stream := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_native\",\"type\":\"function_call\",\"call_id\":\"call_native\",\"namespace\":\"collaboration\",\"name\":\"spawn_agent\",\"arguments\":\"{}\",\"encrypted_function_args\":[\"opaque-arguments\"]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_native\",\"status\":\"completed\",\"output\":[]}}\n\n"
	_, err := writer.Write([]byte(stream))
	require.NoError(t, err)
	require.NoError(t, writer.Finalize())
	assert.Equal(t, stream, sink.Body.String())
}

func TestPortableCodexMixedEncryptedAgentDeliveryRequiresNative(t *testing.T) {
	body := []byte(`{"input":[{"type":"agent_message","author":"parent","recipient":"child","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"opaque-task"}]}]}`)
	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	assert.True(t, converted.Requirements.NativeOnly, "a plaintext routing header cannot replace the encrypted task")
	assert.Empty(t, gjson.GetBytes(converted.Body, "messages").Array())
	assert.Equal(t, body, converted.OriginalBody)
	assertReportCode(t, converted.Report, "responses_agent_message_native_only")
}
