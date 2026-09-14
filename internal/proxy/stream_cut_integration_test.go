package proxy_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const streamCutTurnBody = `{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func captureCompletionLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	// Prime observability's sync.Once before overriding slog.Default.
	observability.Get()
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &logBuf
}

// An upstream that dies after committing output leaves the turn unretryable,
// so the completion line is the only place the cut can be described: how far
// in, which frame was last, and who owned the failure.
func TestProxyMessages_CommittedStreamCutLogsDiagnostics(t *testing.T) {
	logBuf := captureCompletionLog(t)

	provider := &fakeProvider{
		proxyErr: io.ErrUnexpectedEOF,
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			for _, frame := range []string{
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"b\"}}\n\n",
			} {
				_, _ = io.WriteString(w, frame)
			}
		},
	}
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test"},
		map[string]providers.Client{providers.ProviderAnthropic: provider},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(streamCutTurnBody))
	require.Error(t, svc.ProxyMessages(context.Background(), []byte(streamCutTurnBody), rec, req))

	logged := logBuf.String()
	assert.Contains(t, logged, "prelude_committed=true")
	assert.Contains(t, logged, "stream_upstream_frames=3")
	assert.Contains(t, logged, "stream_last_upstream_event=content_block_delta",
		"the router's own error frame must not become the last upstream event")
	assert.Contains(t, logged, "stream_failure_class=upstream_eof")
	assert.Contains(t, logged, "stream_cut_elapsed_ms=")
	assert.Contains(t, logged, "stream_ms_since_last_upstream_frame=")
}

// A client that hangs up mid-stream looks identical on the wire to an upstream
// cut; the class is what tells the two apart.
func TestProxyMessages_ClientCancelClassifiedSeparately(t *testing.T) {
	logBuf := captureCompletionLog(t)

	provider := &fakeProvider{
		proxyErr: context.Canceled,
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			for _, frame := range []string{
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n",
			} {
				_, _ = io.WriteString(w, frame)
			}
		},
	}
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test"},
		map[string]providers.Client{providers.ProviderAnthropic: provider},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(streamCutTurnBody))
	require.Error(t, svc.ProxyMessages(context.Background(), []byte(streamCutTurnBody), rec, req))

	logged := logBuf.String()
	assert.Contains(t, logged, "stream_failure_class=client_canceled")
	assert.Contains(t, logged, "stream_upstream_frames=2")
}

// A turn that streams to completion must add no stream-cut fields at all.
func TestProxyMessages_CleanStreamLogsNoStreamCutFields(t *testing.T) {
	logBuf := captureCompletionLog(t)

	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}}
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test"},
		map[string]providers.Client{providers.ProviderAnthropic: provider},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(streamCutTurnBody))
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(streamCutTurnBody), rec, req))

	assert.NotContains(t, logBuf.String(), "stream_failure_class")
}

// The translated path shares the prelude/commit plumbing, so a cut there must
// be described too — and the client must still get its in-stream error frame.
func TestProxyMessages_TranslatedPathStreamCutLogsDiagnostics(t *testing.T) {
	logBuf := captureCompletionLog(t)

	provider := &fakeProvider{
		proxyErr: io.ErrUnexpectedEOF,
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"committed\"}\n\n")
		},
	}
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "test"},
		map[string]providers.Client{providers.ProviderOpenAI: provider},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(streamCutTurnBody))
	require.Error(t, svc.ProxyMessages(context.Background(), []byte(streamCutTurnBody), rec, req))

	assert.Contains(t, logBuf.String(), "stream_failure_class=upstream_eof")
	assert.Contains(t, rec.Body.String(), "event: error")
}
