package translate_test

import (
	"errors"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesWriter_EmptyTerminalIsRetryable(t *testing.T) {
	for _, finishReason := range []string{"tool_calls", "length", "stop"} {
		t.Run(finishReason, func(t *testing.T) {
			rec := httptest.NewRecorder()
			w := translate.NewResponsesWriter(rec, "synthetic-model")
			require.NoError(t, w.Prelude(true))
			_, err := w.Write([]byte(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"` + finishReason + `"}],"usage":{"prompt_tokens":2,"completion_tokens":24,"total_tokens":26}}

`))
			require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
			assert.True(t, providers.IsRetryable(err))
			assert.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
		})
	}
}

func TestResponsesToOpenAIChatWriter_EmptyTerminalIsRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_empty","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":8192}}}

`))
	require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
	assert.NotContains(t, rec.Body.String(), `data: [DONE]`)
}

func TestResponsesToAnthropicWriter_EmptyTerminalIsRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToAnthropicWriter(rec, "claude-opus-5", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`event: response.incomplete
data: {"type":"response.incomplete","response":{"id":"resp_empty","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}

`))
	require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
	assert.NotContains(t, rec.Body.String(), "event: message_stop")
}

func TestResponsesToOpenAIChatWriter_NonStreamingEmptyTerminalIsError(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(false))
	_, err := w.Write([]byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_empty","status":"completed","output":[]}}

`))
	require.NoError(t, err)
	require.ErrorIs(t, w.Finalize(), providers.ErrUpstreamEmptyCompletion)
	assert.Empty(t, rec.Body.String())
}

func TestResponsesWriter_NativeEmptyTerminalIsRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, err := w.Write([]byte(`{"id":"resp_empty","status":"completed","output":[]}`))
	require.NoError(t, err)
	require.ErrorIs(t, w.Finalize(), providers.ErrUpstreamEmptyCompletion)
	assert.Empty(t, rec.Body.String())
}

func TestResponsesWriter_NativeStreamingEmptyTerminalIsRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.SetPassthroughBadge()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, err := w.Write([]byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_empty","status":"completed","output":[]}}

`))
	require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
}

func TestResponsesWriter_NativeStreamingEmptyTerminalFinalizesAsFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, err := w.Write([]byte(`event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_empty","status":"in_progress","output":[]}}

event: response.completed
data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_empty","status":"completed","output":[]}}

`))
	require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
	require.NoError(t, w.FinalizeError(err))
	assert.Contains(t, rec.Body.String(), `"type":"response.failed"`)
	assert.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
}

func TestResponsesWriter_ArrayContentIsUsable(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.WriteHeader(200)
	_, err := w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"ok"}]},"finish_reason":"stop"}]}`))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	assert.Contains(t, rec.Body.String(), `"text":"ok"`)
}

func TestResponsesWriter_ReasoningOnlyTerminalIsRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	require.NoError(t, w.Prelude(false))
	_, err := w.Write([]byte(`{"choices":[{"message":{"role":"assistant","reasoning_content":"internal"},"finish_reason":"stop"}]}`))
	require.NoError(t, err)
	require.ErrorIs(t, w.Finalize(), providers.ErrUpstreamEmptyCompletion)
}

func TestResponsesToAnthropicWriter_EmptyCompletionUsesAnthropicEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToAnthropicWriter(rec, "claude-opus-5", nil)
	require.NoError(t, w.Prelude(false))
	_, err := w.Write([]byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_empty","status":"completed","output":[]}}

`))
	require.NoError(t, err)
	err = w.Finalize()
	require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
	assert.Empty(t, rec.Body.String())
}

func TestEmptyCompletionErrorUnwrapsRetrySentinel(t *testing.T) {
	err := errors.New("wrapped")
	wrapped := &providers.UpstreamErrorResponse{Status: 502, Cause: errors.Join(providers.ErrUpstreamEmptyCompletion, err)}
	assert.ErrorIs(t, wrapped, providers.ErrUpstreamEmptyCompletion)
	assert.True(t, providers.IsRetryable(wrapped))
}
