package translate_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// openAIOverflowStream is the stream OpenAI's Responses API sends, over HTTP
// 200, for a prompt larger than the model's window: the error fields sit
// under "error", not at the top level.
const openAIOverflowStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]},"sequence_number":0}

event: response.in_progress
data: {"type":"response.in_progress","response":{"id":"resp_1","status":"in_progress","output":[]},"sequence_number":1}

event: error
data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}

event: response.failed
data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."},"output":[]},"sequence_number":3}

`

type responsesTranslatorUnderTest interface {
	http.ResponseWriter
	Prelude(streaming bool) error
	Finalize() error
}

var responsesTranslators = map[string]func(http.ResponseWriter) responsesTranslatorUnderTest{
	"anthropic": func(w http.ResponseWriter) responsesTranslatorUnderTest {
		return translate.NewResponsesToAnthropicWriter(w, "gpt-5-mini", nil)
	},
	"openai_chat": func(w http.ResponseWriter) responsesTranslatorUnderTest {
		return translate.NewResponsesToOpenAIChatWriter(w, "gpt-5-mini", nil)
	},
}

// translateStream feeds stream through a translator the way a provider does,
// returning the first error from Write or, failing that, from Finalize.
func translateStream(t *testing.T, newWriter func(http.ResponseWriter) responsesTranslatorUnderTest, streaming bool, stream string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	w := newWriter(rec)
	require.NoError(t, w.Prelude(streaming))
	if _, err := w.Write([]byte(stream)); err != nil {
		return rec, err
	}
	return rec, w.Finalize()
}

// An in-stream over-window failure must reach dispatch as a 400 rejection
// carrying the overflow code, so it classifies as a context overflow and the
// ingress answers in its client's native prompt-too-long shape.
func TestResponsesTranslators_InStreamOverflowIsA400Rejection(t *testing.T) {
	for name, newWriter := range responsesTranslators {
		for _, streaming := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/streaming=%t", name, streaming), func(t *testing.T) {
				rec, err := translateStream(t, newWriter, streaming, openAIOverflowStream)

				var rejection *providers.UpstreamErrorResponse
				require.ErrorAs(t, err, &rejection)
				assert.Equal(t, http.StatusBadRequest, rejection.Status)
				assert.Equal(t, "context_length_exceeded", gjson.GetBytes(rejection.Body, "error.type").String())
				assert.Contains(t, gjson.GetBytes(rejection.Body, "error.message").String(), "exceeds the context window")
				if !streaming {
					assert.Zero(t, rec.Body.Len(), "dispatch renders the overflow, so nothing is written here")
				}
			})
		}
	}
}

// Other in-stream failures keep the upstream-fault status, and their nested
// message still reaches the client instead of a generic placeholder.
func TestResponsesTranslators_NestedErrorEventKeepsUpstreamMessage(t *testing.T) {
	const stream = `event: error
data: {"type":"error","error":{"type":"server_error","code":"server_error","message":"The server had an error processing your request."},"sequence_number":2}

`
	for name, newWriter := range responsesTranslators {
		t.Run(name+"/streaming", func(t *testing.T) {
			_, err := translateStream(t, newWriter, true, stream)

			var rejection *providers.UpstreamErrorResponse
			require.ErrorAs(t, err, &rejection)
			assert.Equal(t, http.StatusBadGateway, rejection.Status)
			assert.Equal(t, "server_error", gjson.GetBytes(rejection.Body, "error.type").String())
			assert.Equal(t, "The server had an error processing your request.", gjson.GetBytes(rejection.Body, "error.message").String())
		})
		t.Run(name+"/buffered", func(t *testing.T) {
			rec, err := translateStream(t, newWriter, false, stream)

			require.NoError(t, err)
			assert.Equal(t, http.StatusBadGateway, rec.Code)
			assert.Equal(t, "The server had an error processing your request.", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
		})
	}
}

func TestResponsesTranslators_BufferedOverflowAtEOFIsA400Rejection(t *testing.T) {
	const stream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}

event: response.failed
data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window."},"output":[]}}`
	for name, newWriter := range responsesTranslators {
		t.Run(name, func(t *testing.T) {
			_, err := translateStream(t, newWriter, false, stream)

			var rejection *providers.UpstreamErrorResponse
			require.ErrorAs(t, err, &rejection)
			assert.Equal(t, http.StatusBadRequest, rejection.Status)
			assert.Equal(t, "context_length_exceeded", gjson.GetBytes(rejection.Body, "error.type").String())
			assert.Contains(t, gjson.GetBytes(rejection.Body, "error.message").String(), "exceeds the context window")
		})
	}
}

func TestResponsesTranslators_BufferedInvalidTailIsNotOverflow(t *testing.T) {
	const stream = `event: response.failed
data: {"type":"response.failed","response":{"error":{"code":"context_length_exceeded"}`
	for name, newWriter := range responsesTranslators {
		t.Run(name, func(t *testing.T) {
			rec, err := translateStream(t, newWriter, false, stream)

			require.NoError(t, err)
			assert.Equal(t, http.StatusBadGateway, rec.Code)
			assert.NotContains(t, rec.Body.String(), "context_length_exceeded")
		})
	}
}
