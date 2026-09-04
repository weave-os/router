package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nativeResponsesInstallationID = "3f4c1c2e-2b4d-4c3a-9f6d-0d1a2b3c4d5e"

// codexNativeResponsesCtx mimics a Codex turn: the ChatGPT subscription pair
// makes ProxyOpenAIResponses dispatch the caller's original bytes verbatim,
// which is the path that runs no translator.
func codexNativeResponsesCtx() context.Context {
	ctx := context.WithValue(context.Background(), proxy.OpenAISubscriptionContextKey{}, "eyJhbGciOiJSUzI1NiJ9.codex.sig")
	ctx = context.WithValue(ctx, proxy.OpenAIAccountIDContextKey{}, "acct-123")
	ctx = context.WithValue(ctx, proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
	return context.WithValue(ctx, proxy.InstallationIDContextKey{}, nativeResponsesInstallationID)
}

// nativeResponsesStream frames a native Responses SSE turn ending in terminal.
// The frames are split mid-event to mirror the wire, where an SSE event
// routinely spans several writes.
func nativeResponsesStream(terminal string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		stream := "event: response.created\n" +
			`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","model":"gpt-5.6-sol"}}` + "\n\n" +
			"event: response.output_item.added\n" +
			`data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}` + "\n\n" +
			terminal
		for start := 0; start < len(stream); start += 41 {
			end := min(start+41, len(stream))
			_, _ = io.WriteString(w, stream[start:end])
		}
	}
}

// A Codex turn dispatches its original Responses bytes verbatim, so no
// translator runs and the upstream's terminal event is the only record of how
// the turn ended. Before this was captured, every such turn wrote a NULL
// finish_reason — a tool call and a completed answer were indistinguishable.
func TestService_ProxyOpenAIResponses_NativeTurnRecordsTerminalFinishReason(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal string
		want     string
	}{
		{
			name: "tool call turn",
			terminal: "event: response.completed\n" +
				`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","model":"gpt-5.6-sol","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{}","status":"completed"}],"usage":{"input_tokens":120,"output_tokens":34}}}` + "\n\n",
			want: "tool_calls",
		},
		{
			name: "completed answer",
			terminal: "event: response.completed\n" +
				`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","model":"gpt-5.6-sol","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":120,"output_tokens":8}}}` + "\n\n",
			want: "stop",
		},
		{
			name: "output cap reached",
			terminal: "event: response.incomplete\n" +
				`data: {"type":"response.incomplete","sequence_number":2,"response":{"id":"resp_1","status":"incomplete","model":"gpt-5.6-sol","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":120,"output_tokens":64000}}}` + "\n\n",
			want: "length",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := nativeResponsesStream(tc.terminal)
			provider := &fakeProvider{proxyResponse: upstream}
			telemetry := newCaptureTelemetry()
			svc := proxy.NewService(
				&fakeRouter{decision: router.Decision{
					Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", Reason: "test",
				}},
				map[string]providers.Client{providers.ProviderOpenAI: provider},
				nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", telemetry,
			)

			body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"reasoning","id":"rs_0","encrypted_content":"opaque"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
			require.NoError(t, svc.ProxyOpenAIResponses(codexNativeResponsesCtx(), body, rec, req))

			require.Len(t, provider.proxyEndpoints, 1)
			require.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0],
				"the turn under test must take the native Responses path")

			row := telemetry.firstRow(t)
			require.NotNil(t, row.UpstreamFinishReason,
				"a native Responses turn must record the upstream's terminal reason")
			assert.Equal(t, tc.want, *row.UpstreamFinishReason)
		})
	}
}

// A failed terminal event states the turn's outcome through the upstream error,
// not a finish reason; recording one would report a clean completion.
func TestService_ProxyOpenAIResponses_NativeFailedTerminalRecordsNoFinishReason(t *testing.T) {
	terminal := "event: response.failed\n" +
		`data: {"type":"response.failed","sequence_number":2,"response":{"id":"resp_1","status":"failed","model":"gpt-5.6-sol","error":{"code":"server_error","message":"boom"}}}` + "\n\n"
	provider := &fakeProvider{proxyResponse: nativeResponsesStream(terminal)}
	telemetry := newCaptureTelemetry()
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{
			Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", Reason: "test",
		}},
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", telemetry,
	)

	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"reasoning","id":"rs_0","encrypted_content":"opaque"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIResponses(codexNativeResponsesCtx(), body, rec, req))

	row := telemetry.firstRow(t)
	assert.Nil(t, row.UpstreamFinishReason,
		"a failed turn must not report a finish reason")
}
