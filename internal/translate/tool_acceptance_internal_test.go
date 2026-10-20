package translate

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func acceptanceResponse(calls string) []byte {
	return []byte(`{"id":"resp_synthetic","status":"completed","output":` + calls + `}`)
}

func acceptanceEvent(kind string, fields map[string]any) string {
	fields["type"] = kind
	body, _ := json.Marshal(fields)
	return "data: " + string(body) + "\n\n"
}

func TestAcceptedCallsResponsesParity(t *testing.T) {
	for _, target := range []string{"chat", "anthropic"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", target, streaming), func(t *testing.T) {
				calls := `[{"type":"function_call","call_id":"same","name":"Read","arguments":"{}"},{"type":"function_call","call_id":"same","name":"Write","arguments":"{}"},{"type":"function_call","call_id":"","name":"","arguments":"{}"}]`
				rec := httptest.NewRecorder()
				if target == "chat" {
					w := NewResponsesToOpenAIChatWriter(rec, "synthetic", nil)
					require.NoError(t, w.Prelude(streaming))
					_, err := w.Write([]byte(acceptanceEvent("response.completed", map[string]any{"response": json.RawMessage(acceptanceResponse(calls))})))
					require.NoError(t, err)
					require.NoError(t, w.Finalize())
				} else {
					w := NewResponsesToAnthropicWriter(rec, "synthetic", nil)
					require.NoError(t, w.Prelude(streaming))
					_, err := w.Write([]byte(acceptanceEvent("response.completed", map[string]any{"response": json.RawMessage(acceptanceResponse(calls))})))
					require.NoError(t, err)
					require.NoError(t, w.Finalize())
				}
				var ids []string
				if streaming {
					for _, record := range strings.Split(rec.Body.String(), "\n\n") {
						for _, line := range strings.Split(record, "\n") {
							if !strings.HasPrefix(line, "data: {") {
								continue
							}
							root := gjson.Parse(strings.TrimPrefix(line, "data: "))
							if target == "chat" {
								for _, call := range root.Get("choices.0.delta.tool_calls").Array() {
									ids = append(ids, call.Get("id").String())
									assert.NotEmpty(t, call.Get("function.name").String())
								}
							} else if root.Get("content_block.type").String() == "tool_use" {
								ids = append(ids, root.Get("content_block.id").String())
								assert.NotEmpty(t, root.Get("content_block.name").String())
							}
						}
					}
				} else if target == "chat" {
					for _, call := range gjson.Get(rec.Body.String(), "choices.0.message.tool_calls").Array() {
						ids = append(ids, call.Get("id").String())
						assert.NotEmpty(t, call.Get("function.name").String())
					}
				} else {
					for _, call := range gjson.Get(rec.Body.String(), "content").Array() {
						if call.Get("type").String() == "tool_use" {
							ids = append(ids, call.Get("id").String())
							assert.NotEmpty(t, call.Get("name").String())
						}
					}
				}
				require.Len(t, ids, 2)
				assert.NotEmpty(t, ids[0])
				assert.NotEmpty(t, ids[1])
				assert.NotEqual(t, ids[0], ids[1])
			})
		}
	}
}

func TestAcceptedCallsTerminalFallbackAndLateName(t *testing.T) {
	for _, target := range []string{"chat", "anthropic"} {
		for _, lateName := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/late_name=%v", target, lateName), func(t *testing.T) {
				name := "Read"
				if lateName {
					name = ""
				}
				stream := acceptanceEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "name": name}})
				stream += acceptanceEvent("response.function_call_arguments.delta", map[string]any{"output_index": 0, "delta": "not-json"})
				stream += acceptanceEvent("response.completed", map[string]any{"response": json.RawMessage(acceptanceResponse(`[{"type":"function_call","call_id":"late","name":"Read","arguments":"{\"path\":\"synthetic.txt\"}"}]`))})
				rec := httptest.NewRecorder()
				if target == "chat" {
					w := NewResponsesToOpenAIChatWriter(rec, "synthetic", nil)
					require.NoError(t, w.Prelude(true))
					_, err := w.Write([]byte(stream))
					require.NoError(t, err)
					require.NoError(t, w.Finalize())
					assert.Equal(t, 1, w.Summary().ToolUseBlocks)
				} else {
					w := NewResponsesToAnthropicWriter(rec, "synthetic", nil)
					require.NoError(t, w.Prelude(true))
					_, err := w.Write([]byte(stream))
					require.NoError(t, err)
					require.NoError(t, w.Finalize())
					assert.Equal(t, 1, w.Summary().ToolUseBlocks)
				}
				assert.Contains(t, rec.Body.String(), `\"path\":\"synthetic.txt\"`)
			})
		}
	}
}

func TestAcceptedCallsGeneratedIdentityCollision(t *testing.T) {
	l := NewToolCallLedger()
	first := l.Upsert(0, "", "Read")
	second := l.Upsert(1, first.ExternalID, "Write")
	assert.NotEqual(t, first.ExternalID, second.ExternalID)
}

func TestAcceptedCallsBufferedAdaptersSuppressNameless(t *testing.T) {
	chat := []byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"synthetic answer","tool_calls":[{"id":"bad","type":"function","function":{"name":"","arguments":"{}"}}]}}]}`)
	resp, err := chatCompletionToResponse(chat, "resp_synthetic", "synthetic", 1, nil, "", "")
	require.NoError(t, err)
	for _, item := range gjson.GetBytes(resp, "output").Array() {
		assert.NotEqual(t, "function_call", item.Get("type").String())
	}
	gemini, err := GeminiToOpenAIResponse([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"synthetic answer"},{"functionCall":{"name":"","args":{}}}]}}]}`), "synthetic")
	require.NoError(t, err)
	assert.Empty(t, gjson.GetBytes(gemini, "choices.0.message.tool_calls").Array())
	assert.Equal(t, "stop", gjson.GetBytes(gemini, "choices.0.finish_reason").String())
}
