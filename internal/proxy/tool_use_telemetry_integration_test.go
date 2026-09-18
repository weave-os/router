package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolUseHistoryBody is a Claude Code style history: two resolved Bash calls
// (one errored), one resolved Read, and the user's follow-up. The next
// assistant turn is what the fake upstream streams back.
const toolUseHistoryBody = `{"model":"claude-opus-4-7","stream":true,"messages":[` +
	`{"role":"user","content":"run the tests"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"FAIL"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"path":"a_test.go"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"package a"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_3","name":"Bash","input":{"command":"go vet ./..."}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"ok"}]}` +
	`]}`

const toolUseHistoryErrorCounts = `{"Bash":{"calls":2,"errors":1},"Read":{"calls":1,"errors":0}}`

func TestProxyMessages_NativeToolUseStopPersistsLastToolDetail(t *testing.T) {
	const installID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	editInput := `{"file_path":"a_test.go","old_string":"x","new_string":"y"}`
	frames := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_4\",\"name\":\"Read\",\"input\":{}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"b.go\\\"}\"}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_5\",\"name\":\"Edit\",\"input\":{}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"file_path\\\":\\\"a_test.go\\\",\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"old_string\\\":\\\"x\\\",\\\"new_string\\\":\\\"y\\\"}\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":40}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}

	tests := []struct {
		name           string
		frames         []string
		wantName       string
		wantInputBytes *int32
	}{
		{
			name:           "tool_use stop names the last tool and its input size",
			frames:         frames,
			wantName:       "Edit",
			wantInputBytes: ptrTo(int32(len(editInput))),
		},
		{
			name:   "stream cut before message_delta leaves the detail unknown",
			frames: frames[:6],
		},
		{
			name: "end_turn stop leaves the detail unknown",
			frames: append(append([]string{}, frames[:6]...),
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":40}}\n\n",
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-7"}
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				for _, frame := range tt.frames {
					_, _ = w.Write([]byte(frame))
				}
			}}
			telem := newCaptureTelemetry()
			svc := proxy.NewService(
				&fakeRouter{decision: decision},
				map[string]providers.Client{providers.ProviderAnthropic: provider},
				nil, false, nil, nil, false,
				providers.ProviderAnthropic, "claude-opus-4-7", telem,
			)

			ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, installID)
			rec := httptest.NewRecorder()
			httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
			require.NoError(t, svc.ProxyMessages(ctx, []byte(toolUseHistoryBody), rec, httpReq))

			row := telem.firstRow(t)
			assert.Equal(t, tt.wantName, row.LastToolUseName)
			assert.Equal(t, tt.wantInputBytes, row.LastToolUseInputBytes)
			// The per-tool tally is a function of the request history alone,
			// so it is present however the response ended.
			assert.JSONEq(t, toolUseHistoryErrorCounts, string(row.ToolErrorCounts))
			// The observer tees; the tool frames still reach the client intact.
			assert.Contains(t, rec.Body.String(), `"name":"Edit"`)
			assert.Contains(t, rec.Body.String(), `\"old_string\":\"x\"`)
		})
	}
}

func TestProxyMessages_TranslatedToolUseStopPersistsLastToolDetail(t *testing.T) {
	const installID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	arguments := `{"command":"go test ./..."}`
	frames := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"go test ./...\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"Bash","arguments":"{\"command\":\"go test ./...\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"Bash","arguments":"{\"command\":\"go test ./...\"}"}],"usage":{"input_tokens":11,"output_tokens":7}}}`,
	}
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, frame := range frames {
			_, _ = w.Write([]byte("data: " + frame + "\n\n"))
		}
	}}
	telem := newCaptureTelemetry()
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna"}},
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-opus-4-7", telem,
	)

	ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, installID)
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, []byte(toolUseHistoryBody), rec, httpReq))

	row := telem.firstRow(t)
	assert.Equal(t, ptrTo("tool_use"), row.StopReason)
	assert.Equal(t, "Bash", row.LastToolUseName)
	// The translator re-emits the arguments as Anthropic input_json_delta
	// frames, so the size observed on the client side is the argument JSON.
	assert.Equal(t, ptrTo(int32(len(arguments))), row.LastToolUseInputBytes)
	assert.JSONEq(t, toolUseHistoryErrorCounts, string(row.ToolErrorCounts))
}
