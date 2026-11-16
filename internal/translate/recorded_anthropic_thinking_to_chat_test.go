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

func TestCandidate_AnthropicThinkingReachesChatClientsAsReasoningContent(t *testing.T) {
	upstream := `{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[{"type":"thinking","thinking":"Two reads are needed.","signature":"c2ln"},{"type":"text","text":"Reading."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`
	out, err := translate.AnthropicToOpenAIResponse([]byte(upstream), "client-model")
	require.NoError(t, err)
	assert.Equal(t, "Two reads are needed.", gjson.GetBytes(out, "choices.0.message.reasoning_content").String())

	rec := httptest.NewRecorder()
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_2\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Two reads are needed.\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	candidateDrive(t, translate.NewSSETranslator(rec, "client-model", nil), "text/event-stream", stream)
	var reasoning strings.Builder
	for _, frame := range candidateDataFrames(rec.Body.String()) {
		reasoning.WriteString(frame.Get("choices.0.delta.reasoning_content").String())
	}
	assert.Equal(t, "Two reads are needed.", reasoning.String())
}
