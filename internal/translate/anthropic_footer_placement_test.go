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

func TestAnthropicRoutingFooterWriter_PingBeforeMessageDeltaKeepsFooterInAnswerBlock(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewAnthropicRoutingFooterWriter(rec, testFooter)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	stream := buildAnthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":5,"output_tokens":0}}}`) +
		buildAnthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"The answer is 42."}}`) +
		buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		buildAnthropicSSE("ping", `{"type":"ping"}`) +
		buildAnthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`) +
		buildAnthropicSSE("message_stop", `{"type":"message_stop"}`)
	_, err := w.Write([]byte(stream))
	require.NoError(t, err)

	footerIdx, stopIdx := -1, -1
	for i, ev := range splitSSEEvents(rec.Body.String()) {
		if footerIdx < 0 && containsAll(ev, "content_block_delta", "Weave Router feedback") {
			footerIdx = i
		}
		if containsAll(ev, "event: content_block_stop") {
			stopIdx = i
		}
	}
	require.GreaterOrEqual(t, footerIdx, 0, "a ping between the answer's stop and message_delta must not suppress the footer")
	assert.Less(t, footerIdx, stopIdx, "footer must fold into the answer block before its content_block_stop")
}

func TestAnthropicRoutingFooterWriter_FinalNonTextBlockGetsNoFooter(t *testing.T) {
	for _, block := range []string{
		`{"type":"thinking","thinking":""}`,
		`{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}`,
	} {
		rec := httptest.NewRecorder()
		w := translate.NewAnthropicRoutingFooterWriter(rec, testFooter)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		stream := buildAnthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":5,"output_tokens":0}}}`) +
			buildAnthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":`+block+`}`) +
			buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`) +
			buildAnthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`) +
			buildAnthropicSSE("message_stop", `{"type":"message_stop"}`)
		_, err := w.Write([]byte(stream))
		require.NoError(t, err)

		assert.NotContains(t, rec.Body.String(), "Weave Router feedback", "the text footer only folds into a text block: %s", block)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
