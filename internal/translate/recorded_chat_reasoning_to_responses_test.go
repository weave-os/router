package translate_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const reasoningToResponsesUpstream = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"Need both files.\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"file_path\\\":\\\"/tmp/a.go\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_b\",\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"file_path\\\":\\\"/tmp/b.go\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":8,\"total_tokens\":28}}\n\n" +
	"data: [DONE]\n\n"

func reasoningToResponsesDrive(t *testing.T, w interface {
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

func reasoningToResponsesDataFrames(body string) []gjson.Result {
	var out []gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Valid(data) {
			out = append(out, gjson.Parse(data))
		}
	}
	return out
}

func TestCandidate_ChatReasoningReachesResponsesClientsAsAReasoningItem(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "client-model")
	require.NoError(t, w.Prelude(true))
	reasoningToResponsesDrive(t, w, "text/event-stream", reasoningToResponsesUpstream)

	var summary string
	for _, frame := range reasoningToResponsesDataFrames(rec.Body.String()) {
		if frame.Get("type").String() == "response.completed" {
			summary = frame.Get(`response.output.#(type=="reasoning").summary.0.text`).String()
		}
	}
	assert.Equal(t, "Need both files.", summary)
}
