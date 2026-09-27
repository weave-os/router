package translate_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Codex compacts only on an in-stream response.failed carrying
// context_length_exceeded, whether the stream is native passthrough or
// translated, and whether or not it had already started.
func TestResponsesWriter_FailContextOverflow(t *testing.T) {
	for name, setup := range map[string]func(*translate.ResponsesWriter){
		"translated":          func(*translate.ResponsesWriter) {},
		"passthrough":         func(w *translate.ResponsesWriter) { w.SetPassthrough() },
		"translated started":  func(w *translate.ResponsesWriter) { require.NoError(t, w.Prelude(true)) },
		"passthrough started": func(w *translate.ResponsesWriter) { w.SetPassthroughBadge(); require.NoError(t, w.Prelude(true)) },
	} {
		rec := httptest.NewRecorder()
		w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
		setup(w)
		require.NoError(t, w.FailContextOverflow(true, "context_length_exceeded", "prompt is too long"), name)

		out := rec.Body.String()
		assert.Equal(t, http.StatusOK, rec.Code, name)
		assert.Equal(t, 1, strings.Count(out, "event: response.created"), "%s: exactly one stream opening", name)
		assert.Contains(t, out, "event: response.failed", name)
		assert.Contains(t, out, `"code":"context_length_exceeded"`, name)
		assert.NotContains(t, out, "upstream_error", name)
	}
}

func TestResponsesWriter_FailContextOverflowNonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	require.NoError(t, w.FailContextOverflow(false, "context_length_exceeded", "prompt is too long"))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"error":{"message":"prompt is too long","type":"invalid_request_error","param":null,"code":"context_length_exceeded"}}`, rec.Body.String())
}
