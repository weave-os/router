package translate_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

// Synthetic regression from weekly trajectory review: the strict upstream
// schema admits a nested null that the original client's enum rejects.
func TestOptionalToolArgumentsRoundTrip(t *testing.T) {
	const inbound = `{
	  "model":"claude-opus-5-5","max_tokens":4096,
	  "messages":[{"role":"user","content":"Record the synthetic review entry."}],
	  "tools":[{"name":"RecordEntries","input_schema":{
	    "type":"object","properties":{"entries":{"type":"array","items":{
	      "type":"object","properties":{
	        "summary":{"type":"string"},
	        "outcome":{"type":"string","enum":["accepted","skipped"]}
	      },"required":["summary"],"additionalProperties":false
	    }}},"required":["entries"],"additionalProperties":false
	  }}]
	}`
	const modelArguments = `{"entries":[{"summary":"synthetic check","outcome":null}]}`
	const expectedArguments = `{"entries":[{"summary":"synthetic check"}]}`
	envelope, err := translate.ParseAnthropic([]byte(inbound))
	require.NoError(t, err)
	prepared, err := envelope.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
		TargetModel: "gpt-6-luna", Capabilities: router.Lookup("gpt-6-luna"),
	})
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(prepared.Body, "tools.0.strict").Bool())
	outcomeSchema := gjson.GetBytes(prepared.Body, "tools.0.parameters.properties.entries.items.properties.outcome")
	assert.JSONEq(t, `["string","null"]`, outcomeSchema.Get("type").Raw)
	assert.JSONEq(t, `["accepted","skipped",null]`, outcomeSchema.Get("enum").Raw)
	assert.Contains(t, outcomeSchema.Get("description").String(), "pass null to omit")
	validator := envelope.ToolValidator()
	require.NotNil(t, validator)

	item := `{"type":"function_call","call_id":"call_1","name":"RecordEntries","arguments":` + fmt.Sprintf("%q", modelArguments) + `}`
	response := `{"id":"resp_1","status":"completed","output":[` + item + `],"usage":{"input_tokens":10,"output_tokens":5}}`
	responseStream := "data: " + `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"RecordEntries"}}` + "\n\n" +
		"data: " + `{"type":"response.function_call_arguments.delta","output_index":0,"delta":` + fmt.Sprintf("%q", modelArguments[:32]) + "}\n\n" +
		"data: " + `{"type":"response.function_call_arguments.delta","output_index":0,"delta":` + fmt.Sprintf("%q", modelArguments[32:]) + "}\n\n" +
		"data: " + `{"type":"response.output_item.done","output_index":0,"item":` + item + "}\n\n" +
		"data: " + `{"type":"response.completed","response":` + response + "}\n\n"
	chatResponse := `{"id":"chat_1","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"RecordEntries","arguments":` + fmt.Sprintf("%q", modelArguments) + `}}]},"finish_reason":"tool_calls"}]}`
	chatStream := "data: " + `{"id":"chat_1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"RecordEntries","arguments":` + fmt.Sprintf("%q", modelArguments) + `}}]},"finish_reason":null}]}` + "\n\n" +
		"data: " + `{"id":"chat_1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"

	for _, tc := range []struct {
		name       string
		writer     func(http.ResponseWriter) translate.ResponseTranslator
		streamBody string
		jsonBody   string
		argsPath   string
		deltaPath  string
	}{
		{"responses to anthropic", func(w http.ResponseWriter) translate.ResponseTranslator {
			return translate.NewResponsesToAnthropicWriter(w, "gpt-6-luna", nil).WithToolValidator(validator)
		}, responseStream, responseStream, "content.0.input", "delta.partial_json"},
		{"responses to chat", func(w http.ResponseWriter) translate.ResponseTranslator {
			return translate.NewResponsesToOpenAIChatWriter(w, "gpt-6-luna", nil).WithToolValidator(validator)
		}, responseStream, responseStream, "choices.0.message.tool_calls.0.function.arguments", "choices.0.delta.tool_calls.0.function.arguments"},
		{"chat to anthropic", func(w http.ResponseWriter) translate.ResponseTranslator {
			return translate.NewAnthropicSSETranslator(w, "gpt-6-luna", nil).WithToolValidator(validator)
		}, chatStream, chatResponse, "content.0.input", "delta.partial_json"},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, streaming), func(t *testing.T) {
				recorder := httptest.NewRecorder()
				writer := tc.writer(recorder)
				require.NoError(t, writer.Prelude(streaming))
				body := tc.jsonBody
				if streaming {
					body = tc.streamBody
				} else {
					writer.WriteHeader(http.StatusOK)
				}
				// Split transport frames independently of the argument deltas.
				for start := 0; start < len(body); start += 17 {
					_, err := writer.Write([]byte(body[start:min(start+17, len(body))]))
					require.NoError(t, err)
				}
				require.NoError(t, writer.Finalize())
				var arguments string
				if streaming {
					for _, line := range strings.Split(recorder.Body.String(), "\n") {
						if frame, ok := strings.CutPrefix(line, "data: "); ok {
							arguments += gjson.Get(frame, tc.deltaPath).String()
						}
					}
				} else {
					arguments = gjson.GetBytes(recorder.Body.Bytes(), tc.argsPath).String()
				}
				assert.JSONEq(t, expectedArguments, arguments)
				assert.Empty(t, writer.Summary().ToolCallIssues)
			})
		}
	}
}
