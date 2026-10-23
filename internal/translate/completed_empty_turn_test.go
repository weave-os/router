package translate_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/translate"
)

const completedEmptyMessage = `{"id":"resp_synthetic","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","id":"msg_synthetic","role":"assistant","status":"completed","content":[]}],"usage":{"input_tokens":100,"output_tokens":4,"input_tokens_details":{"cached_tokens":80}}}`

func TestResponsesToAnthropicWriter_CompletedEmptyTurn(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for name, prefix := range map[string]string{
			"terminal_only": "",
			"empty_delta":   "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"\"}\n\n",
			"item_events":   "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\n",
		} {
			t.Run(testMode(streaming)+"_"+name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				sink := &completedEmptyUsageSink{}
				w := translate.NewResponsesToAnthropicWriter(rec, "synthetic-model", sink)
				require.NoError(t, w.Prelude(streaming))
				frame := prefix + "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + strings.Replace(completedEmptyMessage, `"content":[]`, `"content":[{"type":"output_text","text":"","annotations":[]}]`, 1) + "}\n\ndata: [DONE]\n\n"
				for _, b := range []byte(frame) {
					_, err := w.Write([]byte{b})
					require.NoError(t, err)
				}
				require.NoError(t, w.Finalize())
				if streaming {
					require.NoError(t, w.Finalize())
				}
				require.Equal(t, 200, rec.Code)
				require.Equal(t, "end_turn", w.Summary().StopReason)
				require.Equal(t, 0, w.Summary().ToolUseBlocks)
				require.Equal(t, 1, sink.calls)
				require.Equal(t, 4, sink.output)
				require.Equal(t, 100, sink.input)
				require.Equal(t, 80, sink.cacheRead)
				if streaming {
					require.Contains(t, rec.Body.String(), `"stop_reason":"end_turn"`)
					require.Contains(t, rec.Body.String(), "event: message_stop")
					require.NotContains(t, rec.Body.String(), "event: content_block_start")
					require.NotContains(t, rec.Body.String(), "event: error")
				} else {
					require.Equal(t, "[]", gjson.GetBytes(rec.Body.Bytes(), "content").Raw)
					require.Equal(t, "end_turn", gjson.GetBytes(rec.Body.Bytes(), "stop_reason").String())
				}
			})
		}
	}
}

func testMode(streaming bool) string {
	if streaming {
		return "streaming"
	}
	return "buffered"
}

func TestResponsesToAnthropicWriter_RejectsUnprovenEmptyTurn(t *testing.T) {
	cases := map[string]string{
		"missing_output":      strings.Replace(completedEmptyMessage, `"output":[{"type":"message","id":"msg_synthetic","role":"assistant","status":"completed","content":[]}],`, "", 1),
		"no_items":            strings.Replace(completedEmptyMessage, `[{"type":"message","id":"msg_synthetic","role":"assistant","status":"completed","content":[]}]`, `[]`, 1),
		"missing_item_status": strings.Replace(completedEmptyMessage, `"status":"completed","content"`, `"content"`, 1),
		"null_item_status":    strings.Replace(completedEmptyMessage, `"status":"completed","content"`, `"status":null,"content"`, 1),
		"missing_content":     strings.Replace(completedEmptyMessage, `,"content":[]`, "", 1),
		"wrong_role":          strings.Replace(completedEmptyMessage, `"role":"assistant"`, `"role":"user"`, 1),
		"in_progress_item":    strings.Replace(completedEmptyMessage, `"status":"completed","content"`, `"status":"in_progress","content"`, 1),
		"refusal":             strings.Replace(completedEmptyMessage, `"content":[]`, `"content":[{"type":"refusal","refusal":""}]`, 1),
		"unknown_content":     strings.Replace(completedEmptyMessage, `"content":[]`, `"content":[{"type":"unknown"}]`, 1),
		"unknown_item":        strings.Replace(completedEmptyMessage, `"type":"message"`, `"type":"unknown"`, 1),
		"incomplete_details":  strings.Replace(completedEmptyMessage, `"incomplete_details":null`, `"incomplete_details":{"reason":"max_output_tokens"}`, 1),
		"response_error":      strings.Replace(completedEmptyMessage, `"error":null`, `"error":{"type":"server_error","message":"failed"}`, 1),
	}
	prefixes := map[string]string{
		"prior_unknown_then_empty_delta": `{"type":"response.refusal.delta","delta":""}` + "\n\ndata: " + `{"type":"response.output_text.delta","output_index":0,"delta":""}`,
		"prior_metadata_semantic_output": `{"type":"response.metadata","output":[{"type":"message"}]}`,
		"prior_metadata_refusal":         `{"type":"codex.response.metadata","refusal":""}`,
		"prior_metadata_error":           `{"type":"codex.rate_limits","error":{"type":"server_error"}}`,
		"prior_arguments_only":           `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
		"prior_refusal":                  `{"type":"response.refusal.delta","delta":""}`,
		"prior_unknown_item":             `{"type":"response.output_item.done","item":{"type":"unknown"}}`,
		"prior_reasoning":                `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`,
		"prior_nameless_tool":            `{"type":"response.output_item.added","item":{"type":"function_call","name":""}}`,
		"prior_created_event_error":      `{"type":"response.created","error":{"type":"server_error"},"response":{"output":[]}}`,
		"prior_empty_delta_event_error":  `{"type":"response.output_text.delta","delta":"","error":{"type":"server_error"}}`,
		"prior_error":                    `{"type":"error","error":{"type":"server_error","message":"failed"}}`,
		"prior_malformed":                `{"type":`,
		"prior_unknown_event":            `{"type":"response.future_semantic_output","delta":""}`,
	}
	for _, streaming := range []bool{false, true} {
		run := func(t *testing.T, body string) {
			rec := httptest.NewRecorder()
			w := translate.NewResponsesToAnthropicWriter(rec, "synthetic-model", nil)
			require.NoError(t, w.Prelude(streaming))
			_, err := w.Write([]byte(body))
			if err == nil {
				err = w.Finalize()
			}
			require.True(t, err != nil || rec.Code >= 400, "invalid empty output must remain an error")
			require.NotContains(t, rec.Body.String(), "event: message_stop")
		}
		for name, r := range cases {
			t.Run(testMode(streaming)+"_"+name, func(t *testing.T) { run(t, "data: {\"type\":\"response.completed\",\"response\":"+r+"}\n\n") })
		}
		for _, field := range []string{"error", "incomplete_details"} {
			t.Run(testMode(streaming)+"_terminal_event_"+field, func(t *testing.T) {
				run(t, "data: {\"type\":\"response.completed\",\""+field+"\":{\"reason\":\"failed\"},\"response\":"+completedEmptyMessage+"}\n\n")
			})
		}
		for name, prefix := range prefixes {
			t.Run(testMode(streaming)+"_"+name, func(t *testing.T) {
				run(t, "data: "+prefix+"\n\ndata: {\"type\":\"response.completed\",\"response\":"+completedEmptyMessage+"}\n\n")
			})
		}
		t.Run(testMode(streaming)+"_incomplete_event", func(t *testing.T) {
			run(t, "data: {\"type\":\"response.incomplete\",\"response\":"+completedEmptyMessage+"}\n\n")
		})
		for _, suffix := range []string{`{"type":"response.metadata","output":[{"type":"message"}]}`, `{"type":"codex.rate_limits","error":{"type":"server_error"}}`, `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`, `{"type":"response.future_semantic_output"}`, `{"type":"response.refusal.delta","delta":""}`, `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`} {
			t.Run(testMode(streaming)+"_after_terminal_"+suffix, func(t *testing.T) {
				run(t, "data: {\"type\":\"response.completed\",\"response\":"+completedEmptyMessage+"}\n\ndata: "+suffix+"\n\n")
			})
		}
		t.Run(testMode(streaming)+"_eof", func(t *testing.T) { run(t, "") })
	}
}

type completedEmptyUsageSink struct {
	fakeUsageSink
	calls int
}

func (s *completedEmptyUsageSink) RecordUsage(input, output int) {
	s.calls++
	s.fakeUsageSink.RecordUsage(input, output)
}

func TestResponsesToAnthropicWriter_MetadataDoesNotInvalidateCompletedEmptyTurn(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, kind := range []string{"codex.rate_limits", "codex.response.metadata", "response.metadata", "responsesapi.websocket_timing"} {
			for _, after := range []bool{false, true} {
				t.Run(testMode(streaming)+"_"+kind+map[bool]string{false: "_before", true: "_after"}[after], func(t *testing.T) {
					rec := httptest.NewRecorder()
					w := translate.NewResponsesToAnthropicWriter(rec, "synthetic-model", nil)
					require.NoError(t, w.Prelude(streaming))
					terminal := "data: {\"type\":\"response.completed\",\"response\":" + completedEmptyMessage + "}\n\n"
					metadata := "data: {\"type\":\"" + kind + "\",\"metadata\":{\"synthetic\":true}}\n\n"
					frame := metadata + terminal
					if after {
						frame = terminal + metadata
					}
					_, err := w.Write([]byte(frame))
					require.NoError(t, err)
					require.NoError(t, w.Finalize())
					require.Equal(t, "end_turn", w.Summary().StopReason)
					if streaming {
						require.NotContains(t, rec.Body.String(), "event: content_block_start")
					} else {
						require.Equal(t, "[]", gjson.GetBytes(rec.Body.Bytes(), "content").Raw)
					}
				})
			}
		}
	}
}
