package translate_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const candidateGeminiSignedCalls = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"Read\",\"args\":{\"file_path\":\"/tmp/a.go\"}},\"thoughtSignature\":\"c3ludGhldGljLWdlbWluaS1zaWduYXR1cmUtMQ==\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}\n\n"

func candidateDrive(t *testing.T, w interface {
	http.ResponseWriter
	Finalize() error
}, contentType, body string) {
	t.Helper()
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
}

func candidateDataFrames(body string) []gjson.Result {
	var out []gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Valid(data) {
			out = append(out, gjson.Parse(data))
		}
	}
	return out
}

func TestCandidate_ResponsesClientKeepsGeminiThoughtSignatureForTheNextTurn(t *testing.T) {
	chat := httptest.NewRecorder()
	candidateDrive(t, translate.NewGeminiToOpenAISSETranslator(chat, "client-model", nil), "text/event-stream", candidateGeminiSignedCalls)
	codex := httptest.NewRecorder()
	responses := translate.NewResponsesWriter(codex, "client-model")
	require.NoError(t, responses.Prelude(true))
	candidateDrive(t, responses, "text/event-stream", chat.Body.String())

	var call gjson.Result
	for _, frame := range candidateDataFrames(codex.Body.String()) {
		if frame.Get("type").String() == "response.output_item.done" && frame.Get("item.type").String() == "function_call" {
			call = frame.Get("item")
		}
	}
	require.True(t, call.Exists(), "Codex never received the function call")

	next := `{"model":"client-model","tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}],"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]},` + call.Raw +
		`,{"type":"function_call_output","call_id":` + call.Get("call_id").Raw + `,"output":"package a"}]}`
	conv, err := translate.ConvertResponsesToChatCompletions([]byte(next))
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(conv.Body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview", TargetProvider: providers.ProviderGoogle})
	require.NoError(t, err, "the second Codex turn of a Gemini 3 tool session must be emittable")
	assert.Equal(t, "c3ludGhldGljLWdlbWluaS1zaWduYXR1cmUtMQ==", gjson.GetBytes(prep.Body, `contents.#(role=="model").parts.0.thoughtSignature`).String())
}
