package translate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/translate"
)

func TestTerminalFailureAdaptsProviderEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name       string
		format     translate.Format
		body       string
		path, code string
	}{
		{"chat to anthropic", translate.FormatAnthropic, `{"error":{"type":"invalid_request_error","message":"synthetic rejection","code":"invalid_parameter"}}`, "error", "invalid_parameter"},
		{"gemini to anthropic", translate.FormatAnthropic, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"synthetic rejection"}}`, "error", "429"},
		{"anthropic to chat", translate.FormatOpenAI, `{"type":"error","error":{"type":"api_error","message":"synthetic rejection","code":"provider_failure"}}`, "error", "provider_failure"},
		{"responses terminal", translate.FormatOpenAIResponses, `{"type":"response.failed","response":{"error":{"code":"provider_failure","message":"synthetic rejection"}}}`, "response.error", "provider_failure"},
		{"responses flat error", translate.FormatOpenAIResponses, `{"type":"error","code":"provider_failure","message":"synthetic rejection"}`, "response.error", "provider_failure"},
		{"SSE HTTP error after prelude", translate.FormatAnthropic, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_synthetic\",\"status\":\"in_progress\"}}\n\nevent: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"provider_failure\",\"message\":\"synthetic rejection\"}}}\n\n", "error", "provider_failure"},
		{"SSE HTTP error", translate.FormatAnthropic, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"synthetic rejection\",\"code\":\"provider_failure\"}}\n\n", "error", "provider_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			caps := translate.NoticeCapabilities{Format: test.format, Streaming: true}
			delivery := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(rec, caps), caps)
			failure := translate.TerminalFailure{Status: 502, Type: "api_error", Code: "upstream_error", Message: "generic", ProviderBody: []byte(test.body)}
			require.NoError(t, delivery.DeliverFailure(failure, true))
			payload := ""
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") {
					payload = strings.TrimPrefix(line, "data: ")
				}
			}
			require.Equal(t, "synthetic rejection", gjson.Get(payload, test.path+".message").Str)
			require.Equal(t, test.code, gjson.Get(payload, test.path+".code").String())
			if test.format == translate.FormatAnthropic {
				require.Equal(t, "error", gjson.Get(payload, "type").Str)
			}
		})
	}
}

func TestOwnedResponsesWaitsForValidatedChatCompletion(t *testing.T) {
	for _, tail := range []string{
		"data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"unfinished\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":1",
		"data: {\"error\":{\"message\":\"synthetic failure\"}}\n\n",
	} {
		rec := httptest.NewRecorder()
		writer := translate.NewResponsesWriter(rec, "synthetic")
		writer.SetFailureOwner()
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, err := writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"total_tokens\":1}}\n\n"))
		require.NoError(t, err)
		require.NotContains(t, rec.Body.String(), "response.completed")
		_, writeErr := writer.Write([]byte(tail))
		finalErr := writer.Finalize()
		require.True(t, writeErr != nil || finalErr != nil)
		require.NotContains(t, rec.Body.String(), "response.completed")
		require.NotContains(t, rec.Body.String(), "response.failed", "request owner delivers failure")
	}
}

func TestResponsesFailureSnapshotRetainsNativePartialItems(t *testing.T) {
	for _, test := range []struct {
		name, item string
		records    []string
		expected   string
	}{
		{"reasoning summary deltas", `{"id":"rs_synthetic","type":"reasoning","encrypted_content":"synthetic","summary":[]}`, []string{
			`{"type":"response.reasoning_summary_text.delta","summary_index":1,"delta":"thinking"}`,
			`{"type":"response.reasoning_summary_text.delta","summary_index":1,"delta":" further"}`,
		}, `{"id":"rs_synthetic","type":"reasoning","encrypted_content":"synthetic","summary":[null,{"type":"summary_text","text":"thinking further"}]}`},
		{"reasoning summary authoritative done", `{"id":"rs_synthetic","type":"reasoning","summary":[]}`, []string{
			`{"type":"response.reasoning_summary_text.delta","summary_index":0,"delta":"partial"}`,
			`{"type":"response.reasoning_summary_text.done","summary_index":0,"text":"authoritative"}`,
		}, `{"id":"rs_synthetic","type":"reasoning","summary":[{"type":"summary_text","text":"authoritative"}]}`},
		{"reasoning text", `{"id":"rs_synthetic","type":"reasoning","content":[]}`, []string{
			`{"type":"response.reasoning_text.delta","content_index":1,"delta":"partial"}`,
			`{"type":"response.reasoning_text.done","content_index":1,"text":"authoritative"}`,
		}, `{"id":"rs_synthetic","type":"reasoning","content":[null,{"type":"reasoning_text","text":"authoritative"}]}`},
		{"content part authoritative done", `{"id":"msg_synthetic","type":"message","role":"assistant","content":[]}`, []string{
			`{"type":"response.output_text.delta","content_index":1,"delta":"partial"}`,
			`{"type":"response.content_part.done","content_index":1,"part":{"type":"output_text","text":"authoritative","annotations":[{"type":"synthetic_annotation"}]}}`,
		}, `{"id":"msg_synthetic","type":"message","role":"assistant","content":[null,{"type":"output_text","text":"authoritative","annotations":[{"type":"synthetic_annotation"}]}]}`},
		{"summary part authoritative done", `{"id":"rs_synthetic","type":"reasoning","summary":[]}`, []string{
			`{"type":"response.reasoning_summary_part.done","summary_index":1,"part":{"type":"summary_text","text":"authoritative"}}`,
		}, `{"id":"rs_synthetic","type":"reasoning","summary":[null,{"type":"summary_text","text":"authoritative"}]}`},
		{"function arguments authoritative done", `{"id":"fc_synthetic","type":"function_call","call_id":"call_synthetic","name":"synthetic","arguments":""}`, []string{
			`{"type":"response.function_call_arguments.delta","delta":"partial"}`,
			`{"type":"response.function_call_arguments.done","arguments":"{}"}`,
		}, `{"id":"fc_synthetic","type":"function_call","call_id":"call_synthetic","name":"synthetic","arguments":"{}"}`},
		{"custom input authoritative done", `{"id":"ct_synthetic","type":"custom_tool_call","call_id":"call_synthetic","name":"synthetic","input":""}`, []string{
			`{"type":"response.custom_tool_call_input.delta","delta":"partial"}`,
			`{"type":"response.custom_tool_call_input.done","input":"authoritative"}`,
		}, `{"id":"ct_synthetic","type":"custom_tool_call","call_id":"call_synthetic","name":"synthetic","input":"authoritative"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writer := translate.NewResponsesWriter(rec, "synthetic")
			writer.SetPassthrough()
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			_, err := fmt.Fprintf(writer, "data: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":%s}\n\n", test.item)
			require.NoError(t, err)
			for _, record := range test.records {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(record), &fields))
				fields["output_index"] = json.RawMessage(`2`)
				fields["item_id"], _ = json.Marshal(gjson.Get(test.item, "id").Str)
				payload, err := json.Marshal(fields)
				require.NoError(t, err)
				_, err = fmt.Fprintf(writer, "data: %s\n\n", payload)
				require.NoError(t, err)
			}
			require.NoError(t, writer.FinalizeError(fmt.Errorf("synthetic transport failure")))
			writer.Flush()
			var terminal string
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") && gjson.Get(strings.TrimPrefix(line, "data: "), "type").Str == "response.failed" {
					terminal = strings.TrimPrefix(line, "data: ")
				}
			}
			require.NotEmpty(t, terminal)
			require.JSONEq(t, test.expected, gjson.Get(terminal, "response.output.0").Raw)
			require.Equal(t, int64(1), gjson.Get(terminal, "response.output.#").Int())
		})
	}
}

func TestNativeResponsesSnapshotAllocationScalesWithOutput(t *testing.T) {
	measure := func(deltas int) int64 {
		return testing.Benchmark(func(b *testing.B) {
			for range b.N {
				writer, _ := newNativeStreamWriter(false)
				_, err := writer.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
				if err != nil {
					b.Fatal(err)
				}
				frame := []byte("data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"synthetic text delta for testing\"}\n\n")
				for range deltas {
					if _, err := writer.Write(frame); err != nil {
						b.Fatal(err)
					}
				}
				if len(writer.FailureSnapshot()) == 0 {
					b.Fatal("missing partial output snapshot")
				}
			}
		}).AllocedBytesPerOp()
	}
	small, large := measure(256), measure(1024)
	t.Logf("snapshot allocation: 256 deltas=%d bytes; 1024 deltas=%d bytes", small, large)
	require.Less(t, large, small*6, "four times the output must not cause quadratic allocation")
}

func TestResponsesFailureSnapshotRejectsAmplifyingIndexes(t *testing.T) {
	for _, event := range []string{
		`{"type":"response.content_part.added","content_index":4096,"part":{"type":"output_text","text":"synthetic"}}`,
		`{"type":"response.reasoning_summary_part.added","summary_index":4096,"part":{"type":"summary_text","text":"synthetic"}}`,
		`{"type":"response.output_text.delta","content_index":4096,"delta":"synthetic"}`,
		`{"type":"response.reasoning_summary_text.delta","summary_index":4096,"delta":"synthetic"}`,
	} {
		t.Run(gjson.Get(event, "type").Str, func(t *testing.T) {
			writer, _ := newNativeStreamWriter(false)
			_, err := writer.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
			require.NoError(t, err)
			_, err = fmt.Fprintf(writer, "data: %s\n\n", event)
			require.NoError(t, err)
			snapshot := writer.FailureSnapshot()
			require.Zero(t, gjson.GetBytes(snapshot, "output.0.content.#").Int())
			require.Zero(t, gjson.GetBytes(snapshot, "output.0.summary.#").Int())
		})
	}
}

func TestResponsesFailureSnapshotIgnoresInvalidIndexesWithoutPoisoningOutput(t *testing.T) {
	for _, fixture := range []struct {
		event, field, item, outputPath string
	}{
		{"response.output_text.delta", "content_index", `{"id":"msg_synthetic","type":"message","role":"assistant","content":[]}`, "output.0.content.0.text"},
		{"response.reasoning_summary_text.delta", "summary_index", `{"id":"rs_synthetic","type":"reasoning","summary":[]}`, "output.0.summary.0.text"},
	} {
		for _, index := range []string{"-1", "2000000000", "9223372036854775807", "1.5", `"1"`} {
			t.Run(fixture.field+"/"+index, func(t *testing.T) {
				writer, _ := newNativeStreamWriter(false)
				_, err := fmt.Fprintf(writer, "data: {\"type\":\"response.output_item.added\",\"item\":%s}\n\n", fixture.item)
				require.NoError(t, err)
				_, err = fmt.Fprintf(writer, "data: {\"type\":%q,%q:%s,\"delta\":\"invalid\"}\n\n", fixture.event, fixture.field, index)
				require.NoError(t, err)
				_, err = fmt.Fprintf(writer, "data: {\"type\":%q,%q:0,\"delta\":\"valid\"}\n\n", fixture.event, fixture.field)
				require.NoError(t, err)
				snapshot := writer.FailureSnapshot()
				require.Equal(t, "valid", gjson.GetBytes(snapshot, fixture.outputPath).Str)
				require.Less(t, len(snapshot), 512)
			})
		}
	}
}
