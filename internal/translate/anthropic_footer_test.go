package translate_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const testFooter = "\n\n_Weave Router feedback:_ `/rf +` good experience · `/rf -` poor experience · note optional, e.g. `/rf - too slow`"

// anthropicAnswerStream is a minimal text answer (index 0) that ends naturally.
func anthropicAnswerStream(stopReason string) string {
	return buildAnthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":0}}}`) +
		buildAnthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"The answer is 42."}}`) +
		buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		buildAnthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+stopReason+`","stop_sequence":null},"usage":{"output_tokens":10}}`) +
		buildAnthropicSSE("message_stop", `{"type":"message_stop"}`)
}

func TestAnthropicRoutingFooterWriter_InjectsBeforeMessageDelta(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicFooterTestWriter(rec, testFooter)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	_, err := w.Write([]byte(anthropicAnswerStream("end_turn")))
	require.NoError(t, err)

	events := splitSSEEvents(rec.Body.String())
	// 6 upstream events + 1 injected footer text_delta = 7. The footer folds
	// into the existing answer block, so no new content_block_start/stop pair.
	require.Len(t, events, 7)

	// Locate the footer text_delta, the answer block's content_block_stop, and
	// the message_delta. The footer must land before both.
	footerIdx, stopIdx, msgDeltaIdx := -1, -1, -1
	for i, e := range events {
		data := extractDataField(e)
		typ := gjson.Get(data, "type").String()
		switch typ {
		case "content_block_delta":
			if strings.Contains(gjson.Get(data, "delta.text").String(), "Weave Router feedback") {
				footerIdx = i
				assert.EqualValues(t, 0, gjson.Get(data, "index").Int(),
					"footer must fold into the answer block (index 0), not a new block")
			}
		case "content_block_stop":
			stopIdx = i
		case "message_delta":
			msgDeltaIdx = i
		}
	}
	require.NotEqual(t, -1, footerIdx, "footer text_delta must be present")
	require.NotEqual(t, -1, stopIdx, "content_block_stop must be present")
	require.NotEqual(t, -1, msgDeltaIdx, "message_delta must be present")
	assert.Less(t, footerIdx, stopIdx, "footer must precede the answer block's content_block_stop")
	assert.Less(t, footerIdx, msgDeltaIdx, "footer must be injected before message_delta")
}

// TestAnthropicRoutingFooterWriter_FoldsIntoFinalTextBlock pins the Conductor
// regression: the footer must share the final answer block's index rather than
// arrive as a standalone trailing block. Clients that render only the last
// content block (Conductor and other Claude Code wrappers) would otherwise show
// the rating prompt as the entire message and bury the answer in the trace.
func TestAnthropicRoutingFooterWriter_FoldsIntoFinalTextBlock(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicFooterTestWriter(rec, testFooter)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	_, err := w.Write([]byte(anthropicAnswerStream("end_turn")))
	require.NoError(t, err)

	for _, e := range splitSSEEvents(rec.Body.String()) {
		data := extractDataField(e)
		// No content block beyond the original answer block (index 0) may exist.
		if strings.HasPrefix(gjson.Get(data, "type").String(), "content_block_") {
			assert.EqualValues(t, 0, gjson.Get(data, "index").Int(),
				"no content block may be added beyond the answer block (index 0)")
		}
	}
}

func TestAnthropicRoutingFooterWriter_SkipsToolUseTurn(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicFooterTestWriter(rec, testFooter)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	_, err := w.Write([]byte(anthropicAnswerStream("tool_use")))
	require.NoError(t, err)

	assert.NotContains(t, rec.Body.String(), "Weave Router feedback", "tool_use turns must not get a footer")
}

func TestAnthropicRoutingFooterWriter_EmptyFooterPassthrough(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicFooterTestWriter(rec, "")
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	in := anthropicAnswerStream("end_turn")
	_, err := w.Write([]byte(in))
	require.NoError(t, err)
	assert.Equal(t, in, rec.Body.String(), "empty footer must be byte-identical passthrough")
}

func TestAnthropicRoutingFooterWriter_NonStreamingPassthrough(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newFooterTestWriter(rec, translate.FormatAnthropic, testFooter, false)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	body := `{"id":"msg_1","content":[{"type":"text","text":"Hi"}]}`
	_, err := w.Write([]byte(body))
	require.NoError(t, err)
	assert.Equal(t, body, rec.Body.String(), "non-streaming must pass through unmodified")
}

func TestAnthropicRoutingFooterWriter_NoContentNoFooter(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicFooterTestWriter(rec, testFooter)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	// message_start then message_delta(end_turn) with no content block at all.
	in := buildAnthropicSSE("message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"x","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`) +
		buildAnthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`) +
		buildAnthropicSSE("message_stop", `{"type":"message_stop"}`)
	_, err := w.Write([]byte(in))
	require.NoError(t, err)
	assert.NotContains(t, rec.Body.String(), "Weave Router feedback", "no content block means nothing to rate")
}

func TestAnthropicRoutingFooterWriter_EventSplitAcrossWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicFooterTestWriter(rec, testFooter)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	full := anthropicAnswerStream("end_turn")
	mid := len(full) / 2
	_, err := w.Write([]byte(full[:mid]))
	require.NoError(t, err)
	_, err = w.Write([]byte(full[mid:]))
	require.NoError(t, err)

	body := rec.Body.String()
	assert.Contains(t, body, "Weave Router feedback", "split stream must still get a footer")
	// Footer must appear exactly once.
	assert.Equal(t, 1, strings.Count(body, "Weave Router feedback"))
}

func TestAnthropicRoutingFooterWriter_WaitsForMessageStop(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicFooterTestWriter(rec, testFooter)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	terminal := buildAnthropicSSE("message_stop", `{"type":"message_stop"}`)
	stream := strings.TrimSuffix(anthropicAnswerStream("end_turn"), terminal)
	_, err := w.Write([]byte(stream))
	require.NoError(t, err)
	assert.NotContains(t, rec.Body.String(), "Weave Router feedback")
	_, err = w.Write([]byte(terminal))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(rec.Body.String(), "Weave Router feedback"))
}

func TestAnthropicRoutingFooterWriter_IncompleteTailDrainsUnchanged(t *testing.T) {
	for _, tail := range []string{"", "event: message_st"} {
		t.Run(tail, func(t *testing.T) {
			rec := httptest.NewRecorder()
			w := newAnthropicFooterTestWriter(rec, testFooter)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			stream := strings.TrimSuffix(anthropicAnswerStream("end_turn"), buildAnthropicSSE("message_stop", `{"type":"message_stop"}`)) + tail
			_, err := w.Write([]byte(stream))
			require.NoError(t, err)
			require.NoError(t, w.Finalize())
			assert.Equal(t, stream, rec.Body.String())
		})
	}
}

func TestAnthropicRoutingFooterWriter_PreservesHeldPingOrder(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicFooterTestWriter(rec, testFooter)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	stop := buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`)
	stream := strings.Replace(anthropicAnswerStream("end_turn"), stop, stop+buildAnthropicSSE("ping", `{"type":"ping"}`), 1)
	_, err := w.Write([]byte(stream))
	require.NoError(t, err)
	assert.Less(t, strings.Index(rec.Body.String(), "Weave Router feedback"), strings.Index(rec.Body.String(), "event: content_block_stop"))
	assert.Less(t, strings.Index(rec.Body.String(), "event: content_block_stop"), strings.Index(rec.Body.String(), "event: ping"))
}

type footerTestWriter struct {
	*translate.CompletionObserver
	finalize func() error
}

func (w *footerTestWriter) Finalize() error {
	if err := w.Finish(nil); err != nil {
		return err
	}
	return w.finalize()
}

func newFooterTestWriter(w http.ResponseWriter, format translate.Format, footer string, streaming bool) *footerTestWriter {
	delivery := translate.NewFeedbackDelivery(context.Background(), translate.FeedbackIntent{Text: footer})
	var adapter interface {
		http.ResponseWriter
		Finalize() error
	}
	switch format {
	case translate.FormatAnthropic:
		adapter = translate.NewAnthropicRoutingFooterWriter(w, delivery)
	case translate.FormatOpenAI:
		adapter = translate.NewOpenAIRoutingFooterWriter(w, delivery)
	case translate.FormatGemini:
		adapter = translate.NewGeminiRoutingFooterWriter(w, delivery)
	}
	return &footerTestWriter{
		CompletionObserver: translate.NewCompletionObserver(adapter, format, streaming, delivery),
		finalize:           adapter.Finalize,
	}
}

func newAnthropicFooterTestWriter(w http.ResponseWriter, footer string) *footerTestWriter {
	return newFooterTestWriter(w, translate.FormatAnthropic, footer, true)
}

func newOpenAIFooterTestWriter(w http.ResponseWriter, footer string) *footerTestWriter {
	return newFooterTestWriter(w, translate.FormatOpenAI, footer, true)
}

func newGeminiFooterTestWriter(w http.ResponseWriter, footer string) *footerTestWriter {
	return newFooterTestWriter(w, translate.FormatGemini, footer, true)
}

func TestAnthropicRoutingFooterWriter_FinalAnswerBeforeTrailingThinking(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicFooterTestWriter(rec, testFooter)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	stop := buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`)
	thinking := buildAnthropicSSE("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`) +
		buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"check"}}`) +
		buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":1}`)
	stream := strings.Replace(anthropicAnswerStream("end_turn"), stop, stop+thinking, 1)
	_, err := w.Write([]byte(stream))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	assert.Equal(t, 1, strings.Count(rec.Body.String(), "Weave Router feedback"))
	assert.Less(t, strings.Index(rec.Body.String(), "Weave Router feedback"), strings.Index(rec.Body.String(), stop))
	assert.Less(t, strings.Index(rec.Body.String(), stop), strings.Index(rec.Body.String(), thinking))
}

func TestAnthropicRoutingFooterWriter_SkipsTrailingBlankTextBlocks(t *testing.T) {
	for _, blank := range []string{"", " "} {
		rec := httptest.NewRecorder()
		w := newAnthropicFooterTestWriter(rec, testFooter)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		stop := buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`)
		trailing := buildAnthropicSSE("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"`+blank+`"}}`) +
			buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" "}}`) +
			buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":1}`)
		stream := strings.Replace(anthropicAnswerStream("end_turn"), stop, stop+trailing, 1)
		_, err := w.Write([]byte(stream))
		require.NoError(t, err)
		require.NoError(t, w.Finalize())
		assert.Equal(t, 1, strings.Count(rec.Body.String(), "Weave Router feedback"))
		for _, event := range splitSSEEvents(rec.Body.String()) {
			payload := extractDataField(event)
			if strings.Contains(gjson.Get(payload, "delta.text").Str, "Weave Router feedback") {
				assert.EqualValues(t, 0, gjson.Get(payload, "index").Int())
			}
		}
		assert.Less(t, strings.Index(rec.Body.String(), "Weave Router feedback"), strings.Index(rec.Body.String(), stop))
		assert.Contains(t, rec.Body.String(), trailing)
	}
}

func TestFeedbackCompletionAdapters_UndelimitedTerminal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format translate.Format
		stream string
	}{
		{"Messages", translate.FormatAnthropic, anthropicAnswerStream("end_turn")},
		{"Chat", translate.FormatOpenAI, strings.TrimSuffix(openAIAnswerStream("stop"), "data: [DONE]\n\n")},
		{"Gemini", translate.FormatGemini, geminiAnswerStream()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			w := newFooterTestWriter(rec, tc.format, testFooter, true)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(strings.TrimRight(tc.stream, "\n")))
			require.NoError(t, err)
			require.NoError(t, w.Finalize())
			assert.Equal(t, 1, strings.Count(rec.Body.String(), "Weave Router feedback"))
		})
	}
}

func TestAnthropicRoutingFooterWriter_DoesNotHoldNoticeLifecycle(t *testing.T) {
	rec := httptest.NewRecorder()
	adapter := translate.NewAnthropicRoutingFooterWriter(rec, translate.NewFeedbackDelivery(context.Background(), translate.FeedbackIntent{Text: testFooter}))
	adapter.Header().Set("Content-Type", "text/event-stream")
	adapter.WriteHeader(http.StatusOK)
	caps := translate.NoticeCapabilities{Format: translate.FormatAnthropic, Streaming: true, Presentation: translate.NoticeText}
	notices := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(adapter, caps), caps)
	_, err := notices.Deliver(translate.NewRouterNotice(translate.NoticeModelSelection, "selected model", "synthetic-model", ""))
	require.NoError(t, err)
	assert.Contains(t, rec.Body.String(), "event: content_block_stop", "notice lifecycle must complete before provider dispatch")
}
