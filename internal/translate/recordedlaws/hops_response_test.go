package recordedlaws_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/require"
)

type hop struct {
	from, to wireFormat
	run      func(t *testing.T, body []byte) []byte
	via      string
}

func (h hop) name() string {
	name := string(h.from) + ">" + string(h.to)
	if h.via != "" {
		name += "@" + h.via
	}
	return name
}

type streamTranslator interface {
	http.ResponseWriter
	Finalize() error
}

func upstreamContentType(body []byte) string {
	if looksLikeSSE(body) {
		return "text/event-stream"
	}
	return "application/json"
}

func drive(t *testing.T, rec *httptest.ResponseRecorder, w streamTranslator, body []byte) []byte {
	t.Helper()
	w.Header().Set("Content-Type", upstreamContentType(body))
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(body)
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	return rec.Body.Bytes()
}

func pure(fn func([]byte, string) ([]byte, error)) func(*testing.T, []byte) []byte {
	return func(t *testing.T, body []byte) []byte {
		t.Helper()
		out, err := fn(body, clientModel)
		require.NoError(t, err)
		return out
	}
}

func viaAnthropicSSE(streaming bool) func(*testing.T, []byte) []byte {
	return func(t *testing.T, body []byte) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		w := translate.NewAnthropicSSETranslator(rec, clientModel, nil)
		require.NoError(t, w.Prelude(streaming))
		return drive(t, rec, w, body)
	}
}

func viaResponsesToAnthropic(streaming bool) func(*testing.T, []byte) []byte {
	return func(t *testing.T, body []byte) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		w := translate.NewResponsesToAnthropicWriter(rec, clientModel, nil).WithReasoningScope(reasoningScope)
		require.NoError(t, w.Prelude(streaming))
		return drive(t, rec, w, body)
	}
}

func viaResponsesToChat(streaming bool) func(*testing.T, []byte) []byte {
	return func(t *testing.T, body []byte) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		w := translate.NewResponsesToOpenAIChatWriter(rec, clientModel, nil)
		require.NoError(t, w.Prelude(streaming))
		return drive(t, rec, w, body)
	}
}

func viaResponsesWriter(streaming bool) func(*testing.T, []byte) []byte {
	return func(t *testing.T, body []byte) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		w := translate.NewResponsesWriter(rec, clientModel)
		require.NoError(t, w.Prelude(streaming))
		return drive(t, rec, w, body)
	}
}

func viaSSETranslator(t *testing.T, body []byte) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	w := translate.NewSSETranslator(rec, clientModel, nil)
	return drive(t, rec, w, body)
}

func viaGeminiSSE(t *testing.T, body []byte) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	w := translate.NewGeminiToOpenAISSETranslator(rec, clientModel, nil)
	return drive(t, rec, w, body)
}

func responsesToAnthropicResponse(body []byte, model string) ([]byte, error) {
	return translate.ResponsesToAnthropicResponse(body, model, reasoningScope)
}

func then(first, second func(*testing.T, []byte) []byte) func(*testing.T, []byte) []byte {
	return func(t *testing.T, body []byte) []byte {
		t.Helper()
		return second(t, first(t, body))
	}
}

func responseHops() []hop {
	return []hop{
		{from: anthropicJSON, to: chatJSON, run: pure(translate.AnthropicToOpenAIResponse)},
		{from: chatJSON, to: anthropicJSON, run: pure(translate.OpenAIToAnthropicResponse)},
		{from: geminiJSON, to: chatJSON, run: pure(translate.GeminiToOpenAIResponse)},
		{from: responsesJSON, to: anthropicJSON, run: pure(responsesToAnthropicResponse)},
		{from: responsesJSON, to: chatJSON, run: pure(translate.ResponsesToOpenAIChatResponse)},
		{from: anthropicSSE, to: chatSSE, run: viaSSETranslator},
		{from: geminiSSE, to: chatSSE, run: viaGeminiSSE},
		{from: chatSSE, to: anthropicSSE, run: viaAnthropicSSE(true)},
		{from: chatJSON, to: anthropicJSON, run: viaAnthropicSSE(false), via: "writer"},
		{from: responsesSSE, to: anthropicSSE, run: viaResponsesToAnthropic(true)},
		{from: responsesSSE, to: anthropicJSON, run: viaResponsesToAnthropic(false)},
		{from: responsesSSE, to: chatSSE, run: viaResponsesToChat(true)},
		{from: responsesSSE, to: chatJSON, run: viaResponsesToChat(false)},
		{from: chatSSE, to: responsesSSE, run: viaResponsesWriter(true)},
		{from: chatJSON, to: responsesJSON, run: viaResponsesWriter(false)},
		{from: anthropicSSE, to: responsesSSE, run: then(viaSSETranslator, viaResponsesWriter(true)), via: "chat"},
		{from: anthropicJSON, to: responsesJSON, run: then(pure(translate.AnthropicToOpenAIResponse), viaResponsesWriter(false)), via: "chat"},
		{from: geminiSSE, to: responsesSSE, run: then(viaGeminiSSE, viaResponsesWriter(true)), via: "chat"},
		{from: geminiJSON, to: responsesJSON, run: then(pure(translate.GeminiToOpenAIResponse), viaResponsesWriter(false)), via: "chat"},
	}
}
