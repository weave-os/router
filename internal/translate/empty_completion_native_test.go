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

type recordingUsageSink struct {
	input, output int
	limit         bool
}

func (s *recordingUsageSink) RecordUsage(inputTokens, outputTokens int) {
	s.input, s.output = inputTokens, outputTokens
}

func (s *recordingUsageSink) RecordCacheUsage(int, int) {}

func (s *recordingUsageSink) RecordOutputLimitReached() { s.limit = true }

func TestResponsesWriter_NativeComputerCallIsUsable(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, err := w.Write([]byte("{\"id\":\"resp_pc\",\"status\":\"completed\",\"output\":[{\"type\":\"computer_call\",\"call_id\":\"c1\",\"action\":{\"type\":\"click\"}}]}"))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "computer_call")
}

func TestResponsesWriter_NativeRefusalIsUsable(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, err := w.Write([]byte("{\"id\":\"resp_ref\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"no\"}]}]}"))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	assert.Contains(t, rec.Body.String(), "refusal")
}

func TestResponsesWriter_QueuedBackgroundIsNotEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, err := w.Write([]byte("{\"id\":\"resp_bg\",\"status\":\"queued\",\"output\":[]}"))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "resp_bg")
}

func TestResponsesWriter_FragmentedNativeSSEDoesNotForwardPartialEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, err := w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_empty\",\"status\":\"completed\",\"out"))
	require.NoError(t, err)
	assert.Empty(t, rec.Body.String())
	_, err = w.Write([]byte("put\":[]}}\n\n"))
	require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
	require.NoError(t, w.FinalizeError(err))
	assert.NotContains(t, rec.Body.String(), "outevent")
	assert.NotContains(t, rec.Body.String(), "response.completed")
}

func TestResponsesWriter_NativeAPIErrorPreservesEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(400)
	_, err := w.Write([]byte("{\"error\":{\"message\":\"too long\",\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\"}}"))
	require.NoError(t, err)
	require.NoError(t, w.FinalizeError(errors.New("upstream 400")))
	assert.Equal(t, 400, rec.Code)
	assert.Contains(t, rec.Body.String(), "context_length_exceeded")
}

func TestResponsesToOpenAIChatWriter_EmptyTerminalRecordsUsage(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &recordingUsageSink{}
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", sink)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_empty\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[],\"usage\":{\"input_tokens\":100,\"output_tokens\":8192}}}\n\n"))
	require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
	assert.Equal(t, 100, sink.input)
	assert.Equal(t, 8192, sink.output)
	assert.True(t, sink.limit)
}

func TestResponsesToAnthropicWriter_EmptyTerminalRecordsUsage(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &recordingUsageSink{}
	w := translate.NewResponsesToAnthropicWriter(rec, "claude-opus-5", sink)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_empty\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[],\"usage\":{\"input_tokens\":100,\"output_tokens\":8192}}}\n\n"))
	require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
	assert.Equal(t, 100, sink.input)
	assert.Equal(t, 8192, sink.output)
	assert.True(t, sink.limit)
}
