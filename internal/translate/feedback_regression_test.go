package translate_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"
)

const regressionFeedback = "\n\n_Weave Router feedback:_ `$rf +` good · `$rf -` poor"

func TestFeedbackCompletionBufferedChatTextArray(t *testing.T) {
	w, recorder := feedbackRegressionWriter(false, false)
	_, err := w.Write([]byte(`{"id":"chat_array","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"answer"}]},"finish_reason":"stop"}]}`))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	assert.Equal(t, "answer"+regressionFeedback, gjson.Get(recorder.Body.String(), "output.0.content.0.text").Str)
}

func TestResponsesFeedbackLongAnswerAllocations(t *testing.T) {
	const deltaCount = 8192
	delta := []byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_long\",\"output_index\":0,\"content_index\":0,\"delta\":\"0123456789abcdef\"}\n\n")
	answer := strings.Repeat("0123456789abcdef", deltaCount)
	terminal := []byte(`data: {"type":"response.completed","response":{"id":"resp_long","status":"completed","output":[{"type":"message","id":"msg_long","role":"assistant","content":[{"type":"output_text","text":"` + answer + `"}]}]}}` + "\n\n")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	w, recorder := feedbackRegressionWriter(true, true)
	for range deltaCount {
		_, err := w.Write(delta)
		require.NoError(t, err)
	}
	_, err := w.Write(terminal)
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("128 KiB answer in 16-byte deltas allocated %d bytes", allocated)
	assert.Less(t, allocated, uint64(96*1024*1024), "stream accumulation must not copy every complete answer prefix")
	footerDeltas := 0
	for _, event := range feedbackRegressionEvents(t, recorder.Body.String()) {
		if event.Get("type").Str == "response.output_text.delta" && strings.Contains(event.Get("delta").Str, "Weave Router feedback") {
			footerDeltas++
		}
	}
	assert.Equal(t, 1, footerDeltas)
}

type feedbackRegressionOutput struct {
	*translate.ResponsesWriter
	source  *translate.CompletionObserver
	adapter *translate.ResponsesFeedbackWriter
}

func (w *feedbackRegressionOutput) Write(body []byte) (int, error) { return w.source.Write(body) }
func (w *feedbackRegressionOutput) Finalize() error {
	if err := w.source.Finish(nil); err != nil {
		return err
	}
	if err := w.ResponsesWriter.Finalize(); err != nil {
		return err
	}
	return w.adapter.Finalize()
}

func feedbackRegressionWriter(native, streaming bool) (*feedbackRegressionOutput, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	delivery := translate.NewFeedbackDelivery(context.Background(), translate.FeedbackIntent{Text: regressionFeedback})
	adapter := translate.NewResponsesFeedbackWriter(recorder, delivery)
	writer := translate.NewResponsesWriter(adapter, "synthetic-model")
	format := translate.FormatOpenAI
	if native {
		writer.SetPassthroughBadge()
		format = translate.FormatOpenAIResponses
	}
	source := translate.NewCompletionObserver(writer, format, streaming, delivery)
	contentType := "application/json"
	if streaming {
		contentType = "text/event-stream"
	}
	writer.Header().Set("Content-Type", contentType)
	writer.WriteHeader(http.StatusOK)
	return &feedbackRegressionOutput{ResponsesWriter: writer, source: source, adapter: adapter}, recorder
}

// configureResponsesFeedback observes the direct writer's emitted Responses wire.
// Conversion tests that need upstream facts use feedbackRegressionWriter instead.
func configureResponsesFeedback(writer *translate.ResponsesWriter, footer string) *configuredResponsesFeedback {
	delivery := translate.NewFeedbackDelivery(context.Background(), translate.FeedbackIntent{Text: footer})
	configured := &configuredResponsesFeedback{}
	writer.WrapInner(func(inner http.ResponseWriter) http.ResponseWriter {
		configured.adapter = translate.NewResponsesFeedbackWriter(inner, delivery)
		configured.source = translate.NewCompletionObserver(configured.adapter, translate.FormatOpenAIResponses, true, delivery)
		return configured.source
	})
	return configured
}

type configuredResponsesFeedback struct {
	source  *translate.CompletionObserver
	adapter *translate.ResponsesFeedbackWriter
}

func (w *configuredResponsesFeedback) Finalize() error {
	if err := w.source.Finish(nil); err != nil {
		return err
	}
	return w.adapter.Finalize()
}

func feedbackRegressionEvents(t *testing.T, body string) []gjson.Result {
	t.Helper()
	var scanner sse.Scanner
	var events []gjson.Result
	pending := []byte(body)
	for len(pending) > 0 {
		event, consumed := scanner.Next(pending)
		require.NotZero(t, consumed, "incomplete output SSE")
		_, payload := sse.ParseEvent(event)
		if len(payload) > 0 {
			require.True(t, gjson.ValidBytes(payload))
			events = append(events, gjson.ParseBytes(payload))
		}
		pending = pending[consumed:]
	}
	return events
}

func feedbackRegressionTextEvents() []string {
	return []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_test","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_test","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_test","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_test","output_index":0,"content_index":0,"delta":"answer"}`,
		`{"type":"response.output_text.done","sequence_number":4,"item_id":"msg_test","output_index":0,"content_index":0,"text":"answer"}`,
		`{"type":"response.content_part.done","sequence_number":5,"item_id":"msg_test","output_index":0,"content_index":0,"part":{"type":"output_text","text":"answer"}}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}}`,
	}
}

func writeFeedbackRegressionNative(t *testing.T, writer *feedbackRegressionOutput, payloads []string) {
	t.Helper()
	var stream strings.Builder
	for _, payload := range payloads {
		stream.WriteString(nativeStreamFrame(payload))
	}
	encoded := stream.String()
	for start := 0; start < len(encoded); start += 17 {
		_, err := writer.Write([]byte(encoded[start:min(start+17, len(encoded))]))
		require.NoError(t, err)
	}
}

func TestFeedbackRegressionResponsesSuppressesIncompleteAndEOF(t *testing.T) {
	for _, terminal := range []string{"incomplete", "EOF"} {
		t.Run(terminal, func(t *testing.T) {
			writer, recorder := feedbackRegressionWriter(true, true)
			payloads := feedbackRegressionTextEvents()
			if terminal != "EOF" {
				payloads = append(payloads, `{"type":"response.incomplete","sequence_number":7,"response":{"id":"resp_test","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`)
			}
			writeFeedbackRegressionNative(t, writer, payloads)
			_ = writer.Finalize()
			assert.NotContains(t, recorder.Body.String(), "Weave Router feedback")
		})
	}
}

func TestFeedbackRegressionResponsesDeltasMatchSnapshots(t *testing.T) {
	writer, recorder := feedbackRegressionWriter(true, true)
	payloads := append(feedbackRegressionTextEvents(), `{"type":"response.completed","sequence_number":7,"response":{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`)
	writeFeedbackRegressionNative(t, writer, payloads)
	require.NoError(t, writer.Finalize())
	events := feedbackRegressionEvents(t, recorder.Body.String())
	var text strings.Builder
	for _, event := range events {
		if event.Get("type").Str == "response.output_text.delta" {
			text.WriteString(event.Get("delta").Str)
		}
	}
	assert.Equal(t, "answer"+regressionFeedback, text.String())
	assert.Equal(t, text.String(), events[len(events)-1].Get("response.output.0.content.0.text").Str)
}

func TestFeedbackRegressionResponsesClosureOrdering(t *testing.T) {
	writer, recorder := feedbackRegressionWriter(true, true)
	payloads := append(feedbackRegressionTextEvents(),
		`{"type":"response.output_item.added","sequence_number":7,"output_index":1,"item":{"id":"rs_test","type":"reasoning","summary":[]}}`,
		`{"type":"response.output_item.done","sequence_number":8,"output_index":1,"item":{"id":"rs_test","type":"reasoning","summary":[]}}`,
		`{"type":"response.completed","sequence_number":9,"response":{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"id":"rs_test","type":"reasoning","summary":[]}]}}`)
	writeFeedbackRegressionNative(t, writer, payloads)
	require.NoError(t, writer.Finalize())
	events := feedbackRegressionEvents(t, recorder.Body.String())
	for index := 1; index < len(events); index++ {
		assert.Greater(t, events[index].Get("sequence_number").Int(), events[index-1].Get("sequence_number").Int(), "event %d: %s", index, events[index].Raw)
	}
}

func TestFeedbackRegressionResponsesOnlyLastTextPart(t *testing.T) {
	writer, recorder := feedbackRegressionWriter(true, true)
	payloads := feedbackRegressionTextEvents()[:6]
	payloads = append(payloads,
		`{"type":"response.content_part.added","sequence_number":6,"item_id":"msg_test","output_index":0,"content_index":1,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","sequence_number":7,"item_id":"msg_test","output_index":0,"content_index":1,"delta":"last"}`,
		`{"type":"response.output_text.done","sequence_number":8,"item_id":"msg_test","output_index":0,"content_index":1,"text":"last"}`,
		`{"type":"response.content_part.done","sequence_number":9,"item_id":"msg_test","output_index":0,"content_index":1,"part":{"type":"output_text","text":"last"}}`,
		`{"type":"response.output_item.done","sequence_number":10,"output_index":0,"item":{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"output_text","text":"last"}]}}`,
		`{"type":"response.completed","sequence_number":11,"response":{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"output_text","text":"last"}]}]}}`)
	writeFeedbackRegressionNative(t, writer, payloads)
	require.NoError(t, writer.Finalize())
	events := feedbackRegressionEvents(t, recorder.Body.String())
	terminal := events[len(events)-1]
	assert.Equal(t, "answer", terminal.Get("response.output.0.content.0.text").Str)
	assert.Equal(t, "last"+regressionFeedback, terminal.Get("response.output.0.content.1.text").Str)
	for _, event := range events {
		if event.Get("type").Str == "response.output_text.done" {
			expected := "answer"
			if event.Get("content_index").Int() == 1 {
				expected = "last" + regressionFeedback
			}
			assert.Equal(t, expected, event.Get("text").Str)
		}
	}
}

func TestFeedbackRegressionResponsesBufferedCompletionPolicy(t *testing.T) {
	t.Run("translated truncation", func(t *testing.T) {
		writer, recorder := feedbackRegressionWriter(false, false)
		_, err := writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}]}`))
		require.NoError(t, err)
		require.NoError(t, writer.Finalize())
		assert.NotContains(t, recorder.Body.String(), "Weave Router feedback")
	})
	t.Run("native natural answer", func(t *testing.T) {
		writer, recorder := feedbackRegressionWriter(true, false)
		_, err := writer.Write([]byte(`{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`))
		require.NoError(t, err)
		require.NoError(t, writer.Finalize())
		assert.Equal(t, "answer"+regressionFeedback, gjson.GetBytes(recorder.Body.Bytes(), "output.0.content.0.text").Str)
	})
}

func TestFeedbackRegressionResponsesSnapshotOnlyReconstructsSuffix(t *testing.T) {
	writer, recorder := feedbackRegressionWriter(true, true)
	writeFeedbackRegressionNative(t, writer, []string{`{"type":"response.completed","sequence_number":0,"response":{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[{"type":"custom_annotation","value":7}]}],"custom_field":"preserved"}]}}`})
	require.NoError(t, writer.Finalize())
	events := feedbackRegressionEvents(t, recorder.Body.String())
	require.Len(t, events, 7)
	assert.Equal(t, "response.output_item.added", events[0].Get("type").Str)
	assert.Equal(t, "response.content_part.added", events[1].Get("type").Str)
	assert.Equal(t, "answer"+regressionFeedback, events[2].Get("delta").Str)
	terminal := events[len(events)-1]
	assert.Equal(t, events[2].Get("delta").Str, terminal.Get("response.output.0.content.0.text").Str)
	assert.Equal(t, "preserved", terminal.Get("response.output.0.custom_field").Str)
	assert.EqualValues(t, 7, terminal.Get("response.output.0.content.0.annotations.0.value").Int())
	assert.EqualValues(t, 7, events[1].Get("part.annotations.0.value").Int())
	assert.EqualValues(t, 7, events[4].Get("part.annotations.0.value").Int())
}

func TestFeedbackRegressionResponsesSnapshotOnlyMultipart(t *testing.T) {
	writer, recorder := feedbackRegressionWriter(true, true)
	writeFeedbackRegressionNative(t, writer, []string{`{"type":"response.completed","sequence_number":0,"response":{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"first"},{"type":"output_text","text":"last"}]}]}}`})
	require.NoError(t, writer.Finalize())
	events := feedbackRegressionEvents(t, recorder.Body.String())
	require.Len(t, events, 10)
	assert.Equal(t, "response.content_part.added", events[1].Get("type").Str)
	assert.EqualValues(t, 0, events[1].Get("content_index").Int())
	assert.Equal(t, "first", events[1].Get("part.text").Str)
	assert.Equal(t, "response.content_part.done", events[3].Get("type").Str)
	assert.Equal(t, "response.content_part.added", events[4].Get("type").Str)
	assert.EqualValues(t, 1, events[4].Get("content_index").Int())
	assert.Equal(t, "last"+regressionFeedback, events[5].Get("delta").Str)
	terminal := events[len(events)-1]
	assert.Equal(t, "first", terminal.Get("response.output.0.content.0.text").Str)
	assert.Equal(t, "last"+regressionFeedback, terminal.Get("response.output.0.content.1.text").Str)
}

func TestFeedbackRegressionResponsesNonemptyPartAddedRemainsVisibleOnce(t *testing.T) {
	writer, recorder := feedbackRegressionWriter(true, true)
	payloads := feedbackRegressionTextEvents()
	payloads[2] = `{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_test","output_index":0,"content_index":0,"part":{"type":"output_text","text":"answer"}}`
	payloads = append(append(payloads[:3], payloads[4:]...), `{"type":"response.completed","sequence_number":7,"response":{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`)
	writeFeedbackRegressionNative(t, writer, payloads)
	require.NoError(t, writer.Finalize())
	events := feedbackRegressionEvents(t, recorder.Body.String())
	var text strings.Builder
	for _, event := range events {
		if event.Get("type").Str == "response.content_part.added" {
			text.WriteString(event.Get("part.text").Str)
		}
		if event.Get("type").Str == "response.output_text.delta" {
			text.WriteString(event.Get("delta").Str)
		}
	}
	assert.Equal(t, "answer"+regressionFeedback, text.String())
	assert.Equal(t, text.String(), events[len(events)-1].Get("response.output.0.content.0.text").Str)
}

func TestFeedbackRegressionResponsesWhitespacePartDoesNotStealFooter(t *testing.T) {
	writer, recorder := feedbackRegressionWriter(true, true)
	payloads := feedbackRegressionTextEvents()[:6]
	payloads = append(payloads,
		`{"type":"response.content_part.added","sequence_number":6,"item_id":"msg_test","output_index":0,"content_index":1,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","sequence_number":7,"item_id":"msg_test","output_index":0,"content_index":1,"delta":"  "}`,
		`{"type":"response.output_text.done","sequence_number":8,"item_id":"msg_test","output_index":0,"content_index":1,"text":"  "}`,
		`{"type":"response.content_part.done","sequence_number":9,"item_id":"msg_test","output_index":0,"content_index":1,"part":{"type":"output_text","text":"  "}}`,
		`{"type":"response.output_item.done","sequence_number":10,"output_index":0,"item":{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"output_text","text":"  "}]}}`,
		`{"type":"response.completed","sequence_number":11,"response":{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"output_text","text":"  "}]}]}}`)
	writeFeedbackRegressionNative(t, writer, payloads)
	require.NoError(t, writer.Finalize())
	events := feedbackRegressionEvents(t, recorder.Body.String())
	terminal := events[len(events)-1]
	assert.Equal(t, "answer"+regressionFeedback, terminal.Get("response.output.0.content.0.text").Str)
	assert.Equal(t, "  ", terminal.Get("response.output.0.content.1.text").Str)
}

func TestFeedbackRegressionResponsesDisabledIsTransparent(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := translate.NewResponsesFeedbackWriter(recorder, translate.NewFeedbackDelivery(context.Background(), translate.FeedbackIntent{}))
	writer.Header().Set("Content-Type", "text/event-stream")
	stream := nativeStreamFrame(`{"type":"response.output_text.done","sequence_number":5,"text":"answer"}`) + nativeStreamFrame(`{"type":"custom_metadata","sequence_number":2}`)
	_, err := writer.Write([]byte(stream))
	require.NoError(t, err)
	assert.Equal(t, stream, recorder.Body.String())
	require.NoError(t, writer.Finalize())
	assert.Equal(t, stream, recorder.Body.String())
}

func TestFeedbackRegressionResponsesNoticeAndReasoningRemainImmediate(t *testing.T) {
	recorder := httptest.NewRecorder()
	delivery := translate.NewFeedbackDelivery(context.Background(), translate.FeedbackIntent{Text: regressionFeedback})
	adapter := translate.NewResponsesFeedbackWriter(recorder, delivery)
	writer := translate.NewResponsesWriter(adapter, "synthetic-model")
	writer.SetPassthroughBadge()
	writer.SetBadgeText("**Weave Router** — synthetic selection")
	require.NoError(t, writer.Prelude(true))
	writer.Flush()
	events := feedbackRegressionEvents(t, recorder.Body.String())
	require.Len(t, events, 7, "router notice lifecycle must finish before provider dispatch")
	assert.Equal(t, "response.output_item.done", events[len(events)-1].Get("type").Str)
	_, err := writer.Write([]byte(nativeStreamFrame(`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_test","type":"reasoning","summary":[]}}`) + nativeStreamFrame(`{"type":"response.reasoning_summary_text.delta","sequence_number":2,"item_id":"rs_test","output_index":0,"summary_index":0,"delta":"checking"}`)))
	require.NoError(t, err)
	assert.Contains(t, recorder.Body.String(), "checking", "provider reasoning must not wait for feedback eligibility")
}

func TestFeedbackRegressionResponsesSparseTerminalKeepsOriginalCoordinates(t *testing.T) {
	writer, recorder := feedbackRegressionWriter(true, true)
	payloads := []string{`{"type":"response.created","sequence_number":0,"response":{"id":"resp_test","status":"in_progress","output":[]}}`, `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_test","type":"reasoning","summary":[]}}`, `{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"id":"rs_test","type":"reasoning","summary":[]}}`,
		`{"type":"response.output_item.added","sequence_number":3,"output_index":1,"item":{"id":"msg_test","type":"message","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":4,"item_id":"msg_test","output_index":1,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_test","output_index":1,"content_index":0,"delta":"answer"}`,
		`{"type":"response.output_text.done","sequence_number":6,"item_id":"msg_test","output_index":1,"content_index":0,"text":"answer"}`,
		`{"type":"response.content_part.done","sequence_number":7,"item_id":"msg_test","output_index":1,"content_index":0,"part":{"type":"output_text","text":"answer"}}`,
		`{"type":"response.output_item.done","sequence_number":8,"output_index":1,"item":{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`,
		`{"type":"response.completed","sequence_number":9,"response":{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`}
	writeFeedbackRegressionNative(t, writer, payloads)
	require.NoError(t, writer.Finalize())
	events := feedbackRegressionEvents(t, recorder.Body.String())
	var text strings.Builder
	for _, event := range events {
		if event.Get("type").Str == "response.output_text.delta" {
			assert.EqualValues(t, 1, event.Get("output_index").Int())
			text.WriteString(event.Get("delta").Str)
		}
	}
	assert.Equal(t, "answer"+regressionFeedback, text.String())
	assert.Equal(t, text.String(), events[len(events)-1].Get("response.output.0.content.0.text").Str)
}
