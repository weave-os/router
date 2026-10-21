package translate_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/translate"
)

const syntheticNoticeKind translate.NoticeKind = "synthetic_status"

var noticeFormats = []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses, translate.FormatGemini}

func noticeDelivery(ctx context.Context, rec http.ResponseWriter, format translate.Format) *translate.NoticeDelivery {
	caps := translate.NoticeCapabilities{Format: format, Presentation: translate.NoticeText, Streaming: true}
	return translate.NewNoticeDelivery(ctx, translate.NewProtocolNoticeSink(rec, caps), caps)
}

func TestNoticeContractAndCapabilities(t *testing.T) {
	first := translate.NewRouterNotice(syntheticNoticeKind, "synthetic status\n\n", "model", "attempt-a")
	second := translate.NewRouterNotice(syntheticNoticeKind, first.Text, first.Model, "attempt-b")
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, translate.NoticeFromRouter, first.Provenance)
	assert.Equal(t, "attempt-a", first.AttemptID)
	for _, format := range noticeFormats {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			rec := httptest.NewRecorder()
			delivery := noticeDelivery(context.Background(), rec, format)
			outcome, err := delivery.Deliver(first)
			require.NoError(t, err)
			assert.Equal(t, translate.NoticeDelivered, outcome)
			assert.Contains(t, rec.Body.String(), "synthetic status")
			before := rec.Body.String()
			outcome, err = delivery.Deliver(first)
			require.NoError(t, err)
			assert.Equal(t, translate.NoticeDuplicate, outcome)
			assert.Equal(t, before, rec.Body.String())
		})
	}
	for _, caps := range []translate.NoticeCapabilities{
		{Format: translate.FormatAnthropic, Streaming: true, Presentation: translate.NoticeHidden},
		{Format: translate.FormatAnthropic, Streaming: false, Presentation: translate.NoticeText},
		{Format: translate.Format(-1), Streaming: true, Presentation: translate.NoticeText},
	} {
		rec := httptest.NewRecorder()
		delivery := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(rec, caps), caps)
		outcome, err := delivery.Deliver(first)
		require.NoError(t, err)
		assert.Equal(t, translate.NoticeSuppressed, outcome)
		assert.Empty(t, rec.Body.String())
	}
}

func TestNoticeWritersEscapeProviderHTMLWithoutChangingJSONText(t *testing.T) {
	const providerText = "<script>alert('provider')</script>&"
	for _, format := range noticeFormats {
		for _, streaming := range []bool{false, true} {
			for _, directSink := range []bool{false, true} {
				t.Run(fmt.Sprintf("%v/streaming=%v/direct=%v", format, streaming, directSink), func(t *testing.T) {
					rec := httptest.NewRecorder()
					caps := translate.NoticeCapabilities{Format: format, Presentation: translate.NoticeText, Streaming: streaming}
					sink := translate.NewProtocolNoticeSink(rec, caps)
					var writer http.ResponseWriter = sink
					if !directSink {
						writer = translate.NewNoticeDelivery(context.Background(), sink, caps)
					}
					payload := `{"text":"<script>alert('provider')</script>&"}`
					contentType := "application/json"
					if streaming {
						payload = "data: " + payload + "\n\n"
						contentType = "text/event-stream"
					}
					writer.Header().Set("Content-Type", "text/html")
					writer.Header().Set("Content-Length", fmt.Sprint(len(payload)))
					writer.WriteHeader(http.StatusOK)
					consumed, err := writer.Write([]byte(payload))
					require.NoError(t, err)
					assert.Equal(t, len(payload), consumed)
					writer.(http.Flusher).Flush()
					assert.NotContains(t, rec.Body.String(), "<script>")
					assert.Equal(t, contentType, rec.Result().Header.Get("Content-Type"))
					assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
					assert.Empty(t, rec.Result().Header.Get("Content-Length"))
					body := rec.Body.String()
					if streaming {
						body = extractDataField(body)
					}
					assert.Equal(t, providerText, gjson.Get(body, "text").String())
				})
			}
		}
	}
}

func TestNoticeDeliveryWaitsForCompleteRecord(t *testing.T) {
	for _, format := range noticeFormats {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			rec := httptest.NewRecorder()
			delivery := noticeDelivery(context.Background(), rec, format)
			delivery.Header().Set("Content-Type", "text/event-stream")
			delivery.WriteHeader(http.StatusOK)
			_, err := delivery.Write([]byte(": fragmented"))
			require.NoError(t, err)
			outcome, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "after record\n\n", "model", ""))
			require.NoError(t, err)
			assert.Equal(t, translate.NoticeQueued, outcome)
			assert.Empty(t, rec.Body.String())
			_, err = delivery.Write([]byte("\r\n\r\n"))
			require.NoError(t, err)
			delivery.Flush()
			assert.True(t, strings.HasPrefix(rec.Body.String(), ": fragmented\r\n\r\n"))
			assert.Contains(t, rec.Body.String(), "after record")
		})
	}
}

func TestNoticeDeliveryCancellationAndDisconnect(t *testing.T) {
	for _, format := range noticeFormats {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			rec := httptest.NewRecorder()
			delivery := noticeDelivery(ctx, rec, format)
			cancel()
			_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "cancelled", "model", ""))
			require.ErrorIs(t, err, context.Canceled)
			_, err = delivery.Write([]byte(": ignored\n\n"))
			require.ErrorIs(t, err, context.Canceled)
			assert.Empty(t, rec.Body.String())
			disconnected := &noticeDisconnectedWriter{header: make(http.Header)}
			delivery = noticeDelivery(context.Background(), disconnected, format)
			_, err = delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "disconnect", "model", ""))
			require.ErrorIs(t, err, errNoticeDisconnect)
			_, err = delivery.Write([]byte(": ignored\n\n"))
			require.ErrorIs(t, err, errNoticeDisconnect)
		})
	}
}

var errNoticeDisconnect = errors.New("synthetic disconnect")

type noticeDisconnectedWriter struct{ header http.Header }

func (w *noticeDisconnectedWriter) Header() http.Header       { return w.header }
func (w *noticeDisconnectedWriter) WriteHeader(int)           {}
func (w *noticeDisconnectedWriter) Write([]byte) (int, error) { return 0, errNoticeDisconnect }
func (w *noticeDisconnectedWriter) Flush()                    {}

func TestNoticeDeliverySerializesConcurrentProducers(t *testing.T) {
	for _, format := range noticeFormats {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			rec := httptest.NewRecorder()
			delivery := noticeDelivery(context.Background(), rec, format)
			var writers sync.WaitGroup
			for i := range 12 {
				writers.Go(func() {
					_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, fmt.Sprintf("notice-%d\n\n", i), "model", ""))
					assert.NoError(t, err)
				})
				writers.Go(func() { _, err := delivery.Write([]byte(": provider keepalive\n\n")); assert.NoError(t, err) })
			}
			writers.Wait()
			delivery.Flush()
			for _, record := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
				for _, line := range strings.Split(record, "\n") {
					if strings.HasPrefix(line, "data: ") {
						assert.True(t, gjson.Valid(line[len("data: "):]), line)
					}
				}
			}
			assert.Equal(t, 12, strings.Count(rec.Body.String(), ": provider keepalive"))
		})
	}
}

func TestResponsesNoticesRetainTerminalItems(t *testing.T) {
	rec := httptest.NewRecorder()
	delivery := noticeDelivery(context.Background(), rec, translate.FormatOpenAIResponses)
	for _, text := range []string{"first status\n\n", "second status\n\n"} {
		_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, text, "model", ""))
		require.NoError(t, err)
	}
	payload := "event: response.completed\ndata: " + `{"type":"response.completed","sequence_number":1,"response":{"id":"provider","status":"completed","output":[{"type":"message","id":"answer","content":[{"type":"output_text","text":"answer"}]}]}}` + "\n\n"
	_, err := delivery.Write([]byte(payload))
	require.NoError(t, err)
	delivery.Flush()
	events := splitSSEEvents(rec.Body.String())
	last := gjson.Parse(extractDataField(events[len(events)-1]))
	output := last.Get("response.output").Array()
	require.Len(t, output, 3)
	assert.Contains(t, output[0].Get("content.0.text").String(), "first status")
	assert.Contains(t, output[1].Get("content.0.text").String(), "second status")
	assert.Equal(t, "answer", output[2].Get("content.0.text").String())
	previous := int64(-1)
	for _, event := range events {
		sequence := gjson.Get(extractDataField(event), "sequence_number").Int()
		assert.Greater(t, sequence, previous)
		previous = sequence
	}
	outcome, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "too late", "model", ""))
	require.NoError(t, err)
	assert.Equal(t, translate.NoticeSuppressed, outcome)
}

func TestResponsesNoticeIdentityAfterProviderPreamble(t *testing.T) {
	rec := httptest.NewRecorder()
	delivery := noticeDelivery(context.Background(), rec, translate.FormatOpenAIResponses)
	const text = "same status\n\n"
	_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, text, "model", "attempt-one"))
	require.NoError(t, err)
	_, err = delivery.Write([]byte("event: response.in_progress\ndata: " + `{"type":"response.in_progress","sequence_number":3,"response":{"id":"provider","status":"in_progress","output":[]}}` + "\n\n"))
	require.NoError(t, err)
	_, err = delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, text, "model", "attempt-two"))
	require.NoError(t, err)
	_, err = delivery.Write([]byte("event: response.completed\ndata: " + `{"type":"response.completed","sequence_number":4,"response":{"id":"provider","status":"completed","output":[{"type":"message","id":"answer","content":[{"type":"output_text","text":"answer"}]}]}}` + "\n\n"))
	require.NoError(t, err)
	delivery.Flush()
	previous := int64(-1)
	var noticeIDs []string
	for _, event := range splitSSEEvents(rec.Body.String()) {
		payload := gjson.Parse(extractDataField(event))
		sequence := payload.Get("sequence_number").Int()
		assert.Greater(t, sequence, previous)
		previous = sequence
		if payload.Get("type").String() == "response.output_item.done" {
			noticeIDs = append(noticeIDs, payload.Get("item.id").String())
		}
		if payload.Get("type").String() == "response.completed" {
			assert.Equal(t, int64(3), payload.Get("response.output.#").Int())
		}
	}
	require.Len(t, noticeIDs, 2)
	assert.NotEqual(t, noticeIDs[0], noticeIDs[1])
}

func TestNoticeDeliverySharedAfterConversion(t *testing.T) {
	const chatStream = "data: " + `{"choices":[{"delta":{"content":"provider answer"},"finish_reason":null}]}` + "\n\ndata: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	const anthropicStream = "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"upstream","usage":{"input_tokens":11}}}` + "\n\nevent: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\nevent: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"provider answer"}}` + "\n\nevent: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\nevent: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
	for _, format := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			rec := httptest.NewRecorder()
			delivery := noticeDelivery(context.Background(), rec, format)
			_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "synthetic policy notice\n\n", "model", "attempt"))
			require.NoError(t, err)
			var converter interface {
				http.ResponseWriter
				Finalize() error
			}
			upstream := chatStream
			switch format {
			case translate.FormatAnthropic:
				converter = translate.NewAnthropicSSETranslator(delivery, "model", nil)
			case translate.FormatOpenAI:
				converter = translate.NewSSETranslator(delivery, "model", nil)
				upstream = anthropicStream
			case translate.FormatOpenAIResponses:
				converter = translate.NewResponsesWriter(delivery, "model")
			}
			converter.Header().Set("Content-Type", "text/event-stream")
			converter.WriteHeader(http.StatusOK)
			for _, fragment := range []byte(upstream) {
				_, err = converter.Write([]byte{fragment})
				require.NoError(t, err)
			}
			require.NoError(t, converter.Finalize())
			delivery.Flush()
			assert.Contains(t, rec.Body.String(), "provider answer")
			textDeltas := 0
			for _, event := range splitSSEEvents(rec.Body.String()) {
				payload := gjson.Parse(extractDataField(event))
				text := payload.Get("delta.text").String() + payload.Get("choices.0.delta.content").String() + payload.Get("delta").String()
				if strings.Contains(text, "synthetic policy notice") {
					textDeltas++
				}
			}
			assert.Equal(t, 1, textDeltas)
		})
	}
}

func TestNoticeDoesNotMarkProviderProgress(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			delivery := noticeDelivery(context.Background(), httptest.NewRecorder(), format)
			delivery.Header().Set("Content-Type", "text/event-stream")
			delivery.WriteHeader(http.StatusOK)
			if format == translate.FormatAnthropic {
				_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "first notice", "model", ""))
				require.NoError(t, err)
			}
			progress := 0
			require.True(t, delivery.ArmOutputProgress(func() { progress++ }))
			_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "second notice", "model", ""))
			require.NoError(t, err)
			assert.Zero(t, progress)
			payload := "data: " + `{"choices":[{"delta":{"content":"answer"}}]}` + "\n\n"
			if format == translate.FormatAnthropic {
				payload = "event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}` + "\n\n"
			}
			_, err = delivery.Write([]byte(payload))
			require.NoError(t, err)
			assert.Equal(t, 1, progress)
			outcome, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "after content", "model", ""))
			require.NoError(t, err)
			assert.Equal(t, translate.NoticeSuppressed, outcome)
		})
	}
}

func TestNoticeReplayRecognition(t *testing.T) {
	for _, format := range noticeFormats {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			rec := httptest.NewRecorder()
			delivery := noticeDelivery(context.Background(), rec, format)
			_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "temporary synthetic status", "model", ""))
			require.NoError(t, err)
			textPath := "delta.text"
			switch format {
			case translate.FormatOpenAI:
				textPath = "choices.0.delta.content"
			case translate.FormatOpenAIResponses:
				textPath = "delta"
			case translate.FormatGemini:
				textPath = "candidates.0.content.parts.0.text"
			}
			noticeText := ""
			for _, event := range splitSSEEvents(rec.Body.String()) {
				candidate := gjson.Get(extractDataField(event), textPath)
				if candidate.Type == gjson.String && strings.Contains(candidate.Str, "temporary synthetic status") {
					noticeText = candidate.Str
					break
				}
			}
			require.NotEmpty(t, noticeText)
			assert.True(t, translate.IsRouterNoticeText(noticeText))
			quoted := fmt.Sprintf("%q", noticeText+"genuine answer")
			body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"genuine thinking","signature":"provider signature"},{"type":"text","text":` + quoted + `}]}]}`)
			stripped, err := translate.StripRoutingMarkerFromMessages(body)
			switch format {
			case translate.FormatGemini:
				body = []byte(`{"contents":[{"role":"model","parts":[{"text":` + quoted + `,"thoughtSignature":"provider signature"}]}]}`)
				stripped, err = translate.StripRoutingMarkerFromGeminiContents(body)
			case translate.FormatOpenAIResponses:
				body = []byte(`{"input":[{"role":"assistant","type":"message","content":[{"type":"output_text","text":` + quoted + `}]}]}`)
				stripped, err = translate.StripRoutingBadgeFromResponsesInput(body)
			}
			require.NoError(t, err)
			assert.NotContains(t, string(stripped), "temporary synthetic status")
			assert.Contains(t, string(stripped), "genuine answer")
			if format == translate.FormatAnthropic || format == translate.FormatGemini {
				assert.Contains(t, string(stripped), "provider signature")
			}
		})
	}
}

func TestNoticeReplayPreservesUnmarkedQuotedHeadings(t *testing.T) {
	const quotedText = "Please explain this heading: **WEAVE ROUTER** — an ordinary heading\n\nKeep this paragraph."
	for _, role := range []string{"user", "assistant"} {
		t.Run("messages/"+role, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"` + role + `","content":` + fmt.Sprintf("%q", quotedText) + `}]}`)
			stripped, err := translate.StripRoutingMarkerFromMessages(body)
			require.NoError(t, err)
			assert.Equal(t, quotedText, gjson.GetBytes(stripped, "messages.0.content").String())
		})
	}
	for _, role := range []string{"user", "model"} {
		t.Run("gemini/"+role, func(t *testing.T) {
			body := []byte(`{"contents":[{"role":"` + role + `","parts":[{"text":` + fmt.Sprintf("%q", quotedText) + `}]}]}`)
			stripped, err := translate.StripRoutingMarkerFromGeminiContents(body)
			require.NoError(t, err)
			assert.Equal(t, quotedText, gjson.GetBytes(stripped, "contents.0.parts.0.text").String())
		})
	}
}
