package translate_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicSSEOutputProgress_WhitespaceOnlyContentCounts(t *testing.T) {
	w, count := newChatStreamingWriter(t)
	_, err := w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" \"},\"finish_reason\":null}]}\n\n"))
	require.NoError(t, err)
	assert.Positive(t, *count, "whitespace-only content is real output, not a keepalive")
}

func TestAnthropicSSETranslator_UpstreamErrorBodyIsBounded(t *testing.T) {
	huge := strings.Repeat("x", 20<<10)
	body := driveAnthropicSSEUpstreamError(t, http.StatusInternalServerError, `{"error":{"message":"`+huge+`"}}`)
	assert.Contains(t, body, "event: error")
	assert.Less(t, len(body), 16<<10, "post-Prelude upstream error bodies are buffered up to a fixed cap")
}

func TestAnthropicSSETranslator_StreamingDropsStaleContentLengthAndEncoding(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Length", "42")
	rec.Header().Set("Content-Encoding", "gzip")
	tr := translate.NewAnthropicSSETranslator(rec, "gpt-4o", nil)
	tr.Header().Set("Content-Type", "text/event-stream")
	tr.WriteHeader(http.StatusOK)
	assert.Empty(t, rec.Header().Get("Content-Length"))
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
}

func TestAnthropicSSETranslator_NonPositiveInputTokenEstimateIsIgnored(t *testing.T) {
	rec := httptest.NewRecorder()
	tr := translate.NewAnthropicSSETranslator(rec, "gpt-4o", nil).
		WithEstimatedInputTokens(1234).
		WithEstimatedInputTokens(0).
		WithEstimatedInputTokens(-5)
	tr.Header().Set("Content-Type", "text/event-stream")
	tr.WriteHeader(http.StatusOK)
	_, err := tr.Write([]byte("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
	require.NoError(t, err)
	assert.Contains(t, rec.Body.String(), `"usage":{"input_tokens":1234,"output_tokens":0}`)
}

func TestAnthropicSSETranslator_ReasoningAfterTextClosesTextBlockFirst(t *testing.T) {
	rec := httptest.NewRecorder()
	tr := translate.NewAnthropicSSETranslator(rec, "deepseek-v3.2", nil)
	tr.Header().Set("Content-Type", "text/event-stream")
	tr.WriteHeader(http.StatusOK)
	for _, f := range []string{
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\"},\"finish_reason\":null}]}\n\n",
	} {
		_, err := tr.Write([]byte(f))
		require.NoError(t, err)
	}
	body := rec.Body.String()
	stop := strings.Index(body, "event: content_block_stop")
	thinking := strings.Index(body, `"type":"thinking"`)
	require.GreaterOrEqual(t, thinking, 0)
	require.GreaterOrEqual(t, stop, 0, "the open text block must be closed before a thinking block opens")
	assert.Less(t, stop, thinking)
}

func TestSSETranslator_FlushIsNoOpUntilStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	tr := translate.NewSSETranslator(rec, "m", nil)
	tr.Header().Set("Content-Type", "application/json")
	tr.WriteHeader(http.StatusOK)
	tr.Flush()
	assert.False(t, rec.Flushed)
}

func driveGeminiStream(t *testing.T, chunks ...string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	tr := translate.NewGeminiToOpenAISSETranslator(rec, "gemini-x", nil)
	tr.Header().Set("Content-Type", "text/event-stream")
	tr.WriteHeader(http.StatusOK)
	for _, c := range chunks {
		_, err := tr.Write([]byte("data: " + c + "\n\n"))
		require.NoError(t, err)
	}
	return rec.Body.String()
}

func TestGeminiStream_FirstFunctionCallSignatureLatchesToLaterCalls(t *testing.T) {
	body := driveGeminiStream(t,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"a","args":{}},"thoughtSignature":"SIG"},{"functionCall":{"name":"b","args":{}}}]},"finishReason":"STOP"}]}`)
	encoded := base64.RawURLEncoding.EncodeToString([]byte("SIG"))
	assert.Equal(t, 2, strings.Count(body, encoded), "Gemini 3.x signs only the first call; every call must replay it")
}

func TestGeminiStream_TextPartSignatureLatchesToFunctionCall(t *testing.T) {
	body := driveGeminiStream(t,
		`{"candidates":[{"content":{"parts":[{"text":"hi","thoughtSignature":"TSIG"},{"functionCall":{"name":"a","args":{}}}]},"finishReason":"STOP"}]}`)
	assert.Contains(t, body, base64.RawURLEncoding.EncodeToString([]byte("TSIG")))
}

func TestGeminiStream_UsageOnlyTrailingChunkIsForwarded(t *testing.T) {
	body := driveGeminiStream(t,
		`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`,
		`{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`)
	assert.Contains(t, body, `"prompt_tokens":7`)
}

func TestGeminiStream_NullFunctionArgsBecomeEmptyObject(t *testing.T) {
	body := driveGeminiStream(t,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"a","args":null}}]},"finishReason":"STOP"}]}`)
	assert.Contains(t, body, `"arguments":"{}"`)
}

func TestStreamLifecycle_FailAndCancelRequireStarted(t *testing.T) {
	require.ErrorIs(t, translate.NewStreamLifecycle().Fail(), translate.ErrStreamOrder)
	require.ErrorIs(t, translate.NewStreamLifecycle().Cancel(), translate.ErrStreamOrder)
	l := translate.NewStreamLifecycle()
	require.NoError(t, l.Start())
	require.NoError(t, l.Terminal())
	require.ErrorIs(t, l.Fail(), translate.ErrStreamOrder)
	assert.ErrorIs(t, l.Cancel(), translate.ErrStreamOrder)
}
