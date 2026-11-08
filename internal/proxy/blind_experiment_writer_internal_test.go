package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const blindRequestBody = `{"model":"claude-opus-4-7","messages":[]}`

func TestBlindExperimentWriterReportsRequestedModelInStream(t *testing.T) {
	rec := httptest.NewRecorder()
	w, finish := withBlindExperimentWriter(blindExperimentCtx(auth.BlindExperimentArmRouterOn), rec, []byte(blindRequestBody))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-router-model", "gpt-5.4-mini")
	w.Header().Set("X-Router-Decision", "cluster_argmax")
	w.Header().Set("Access-Control-Expose-Headers", "x-router-model")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"gpt-5"))
	require.NoError(t, err)
	_, err = w.Write([]byte(".4-mini\",\"content\":[]}}\n\nevent: ping\ndata: {\"type\":\"ping\"}\n\n"))
	require.NoError(t, err)
	w.(http.Flusher).Flush()
	finish()

	out := rec.Body.String()
	assert.NotContains(t, out, "gpt-5.4-mini")
	assert.Contains(t, out, `"model":"claude-opus-4-7"`)
	assert.Contains(t, out, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
	assert.Empty(t, rec.Header().Get("x-router-model"))
	assert.Empty(t, rec.Header().Get("X-Router-Decision"))
	assert.Empty(t, rec.Header().Get("Access-Control-Expose-Headers"))
}

func TestBlindExperimentWriterReportsRequestedModelInJSON(t *testing.T) {
	served := `{"id":"m","type":"message","model":"gpt-5.4-mini","content":[{"type":"text","text":"hi"}]}`
	rec := httptest.NewRecorder()
	w, finish := withBlindExperimentWriter(blindExperimentCtx(auth.BlindExperimentArmRouterOn), rec, []byte(blindRequestBody))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", "84")
	w.Header().Set("x-router-provider", "openai")
	_, err := w.Write([]byte(served))
	require.NoError(t, err)
	finish()

	assert.Equal(t, "claude-opus-4-7", gjson.Get(rec.Body.String(), "model").String())
	assert.Equal(t, "hi", gjson.Get(rec.Body.String(), "content.0.text").String())
	assert.Empty(t, rec.Header().Get("Content-Length"))
	assert.Empty(t, rec.Header().Get("x-router-provider"))
}

func TestBlindExperimentWriterRewritesResponsesAndChatModels(t *testing.T) {
	rec := httptest.NewRecorder()
	w, finish := withBlindExperimentWriter(blindExperimentCtx(auth.BlindExperimentArmPassthrough), rec, []byte(`{"model":"gpt-5.5"}`))
	w.Header().Set("Content-Type", "text/event-stream")
	_, err := w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-5.4-mini\"}}\n\ndata: {\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4-mini\"}\n\ndata: [DONE]\n\n"))
	require.NoError(t, err)
	finish()

	assert.NotContains(t, rec.Body.String(), "gpt-5.4-mini")
	assert.Equal(t, 2, strings.Count(rec.Body.String(), `"model":"gpt-5.5"`))
	assert.Contains(t, rec.Body.String(), "data: [DONE]\n\n")
}

func TestBlindExperimentWriterStripsHeadersOnUnwrittenError(t *testing.T) {
	rec := httptest.NewRecorder()
	w, finish := withBlindExperimentWriter(blindExperimentCtx(auth.BlindExperimentArmRouterOn), rec, []byte(blindRequestBody))
	w.Header().Set("x-router-model", "gpt-5.4-mini")
	finish()
	assert.Empty(t, rec.Header().Get("x-router-model"))
}

func TestBlindExperimentWriterInactiveLeavesResponseUntouched(t *testing.T) {
	rec := httptest.NewRecorder()
	w, finish := withBlindExperimentWriter(context.Background(), rec, []byte(blindRequestBody))
	assert.Same(t, http.ResponseWriter(rec), w)
	w.Header().Set("x-router-model", "gpt-5.4-mini")
	_, err := w.Write([]byte(`{"model":"gpt-5.4-mini"}`))
	require.NoError(t, err)
	finish()
	assert.Equal(t, "gpt-5.4-mini", rec.Header().Get("x-router-model"))
	assert.Equal(t, `{"model":"gpt-5.4-mini"}`, rec.Body.String())
}
