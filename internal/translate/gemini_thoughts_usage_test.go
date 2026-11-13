package translate_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

// Gemini reports thinking separately from candidatesTokenCount but bills it at
// the output rate; the OpenAI-shaped completion_tokens must carry the sum or
// every thinking turn is under-billed and under-reported downstream.
const geminiThoughtsUsage = `"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7,"thoughtsTokenCount":250,"totalTokenCount":357}`

func TestGeminiToOpenAIResponse_CompletionTokensIncludeThoughts(t *testing.T) {
	body := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP","index":0}],` + geminiThoughtsUsage + `}`)
	out, err := translate.GeminiToOpenAIResponse(body, "gemini-3.8-flash")
	require.NoError(t, err)
	usage := mustUnmarshal(t, out)["usage"].(map[string]any)
	assert.EqualValues(t, 100, usage["prompt_tokens"])
	assert.EqualValues(t, 257, usage["completion_tokens"])
	assert.EqualValues(t, 357, usage["total_tokens"])
}

func TestGeminiSSETranslator_StreamingCompletionTokensIncludeThoughts(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	translator := translate.NewGeminiToOpenAISSETranslator(rec, "gemini-3.8-flash", sink)
	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)

	chunks := []string{
		`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}` + "\n\n",
		`data: {"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],` + geminiThoughtsUsage + `}` + "\n\n",
	}
	for _, c := range chunks {
		_, err := translator.Write([]byte(c))
		require.NoError(t, err)
	}
	require.NoError(t, translator.Finalize())

	assert.Equal(t, 100, sink.input)
	assert.Equal(t, 257, sink.output)
	assert.Contains(t, rec.Body.String(), `"completion_tokens":257`)
}

func TestGeminiSSETranslator_NonStreamingSinkOutputIncludesThoughts(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	translator := translate.NewGeminiToOpenAISSETranslator(rec, "gemini-3.8-flash", sink)
	translator.Header().Set("Content-Type", "application/json")
	translator.WriteHeader(http.StatusOK)

	body := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],` + geminiThoughtsUsage + `}`
	_, err := translator.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, translator.Finalize())

	assert.Equal(t, 100, sink.input)
	assert.Equal(t, 257, sink.output)
}

func TestGeminiToAnthropicChain_ReasoningTokensReachOuterSink(t *testing.T) {
	for name, tc := range map[string]struct {
		contentType string
		chunks      []string
	}{
		"stream": {"text/event-stream", []string{
			`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}` + "\n\n",
			`data: {"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],` + geminiThoughtsUsage + `}` + "\n\n",
		}},
		"json": {"application/json", []string{
			`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],` + geminiThoughtsUsage + `}`,
		}},
	} {
		sink := &fakeUsageSink{}
		anthropicTranslator := translate.NewAnthropicSSETranslator(httptest.NewRecorder(), "gemini-3.8-flash", sink)
		geminiTranslator := translate.NewGeminiToOpenAISSETranslator(anthropicTranslator, "gemini-3.8-flash", nil)
		geminiTranslator.Header().Set("Content-Type", tc.contentType)
		geminiTranslator.WriteHeader(http.StatusOK)
		for _, c := range tc.chunks {
			_, err := geminiTranslator.Write([]byte(c))
			require.NoError(t, err, name)
		}
		require.NoError(t, geminiTranslator.Finalize(), name)
		require.NoError(t, anthropicTranslator.Finalize(), name)
		assert.Equal(t, 257, sink.output, name)
		assert.Equal(t, 250, sink.reasoning, name)
	}
}
