package translate_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const parallelToolBlocksUpstream = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"Need both files.\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"file_path\\\":\\\"/tmp/a.go\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_b\",\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"file_path\\\":\\\"/tmp/b.go\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":8,\"total_tokens\":28}}\n\n" +
	"data: [DONE]\n\n"

func parallelToolBlocksDrive(t *testing.T, w interface {
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

func parallelToolBlocksDataFrames(body string) []gjson.Result {
	var out []gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Valid(data) {
			out = append(out, gjson.Parse(data))
		}
	}
	return out
}

func TestCandidate_AnthropicSSETranslatorClosesEachToolUseBlockBeforeTheNext(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewAnthropicSSETranslator(rec, "client-model", nil)
	require.NoError(t, w.Prelude(true))
	parallelToolBlocksDrive(t, w, "text/event-stream", parallelToolBlocksUpstream)

	open := int64(-1)
	for _, frame := range parallelToolBlocksDataFrames(rec.Body.String()) {
		switch frame.Get("type").String() {
		case "content_block_start":
			require.Equal(t, int64(-1), open, "block %d starts while block %d is still open", frame.Get("index").Int(), open)
			open = frame.Get("index").Int()
		case "content_block_delta":
			require.Equal(t, open, frame.Get("index").Int(), "delta for a block that is not the open one")
		case "content_block_stop":
			open = -1
		}
	}
}
