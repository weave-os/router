package translate_test

import (
	"context"
	"fmt"
	"github.com/tidwall/gjson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

func TestNoticeDeliveryDrainsCompleteTerminalAtEOF(t *testing.T) {
	const terminal = `data: {"candidates":[{"index":0,"finishReason":"STOP"}]}`
	recorder := httptest.NewRecorder()
	delivery := noticeDelivery(context.Background(), recorder, translate.FormatGemini)
	_, err := delivery.Write([]byte(terminal))
	require.NoError(t, err)
	if finalizer, ok := any(delivery).(interface{ Finalize() error }); ok {
		require.NoError(t, finalizer.Finalize())
	} else {
		delivery.Flush()
	}
	require.Contains(t, recorder.Body.String(), terminal)
	require.True(t, strings.HasSuffix(recorder.Body.String(), "\n\n"))
}

func syntheticTerminalFailure() translate.TerminalFailure {
	return translate.TerminalFailure{Status: http.StatusTooManyRequests, Message: "synthetic subscription pool exhausted", Type: "rate_limit_error", Code: "upstream_error", GeminiStatus: "RESOURCE_EXHAUSTED"}
}

func TestNoticeFailureDeliveryUsesNativeEnvelopesOnce(t *testing.T) {
	for _, format := range noticeFormats {
		for _, streaming := range []bool{false, true} {
			for _, prelude := range []bool{false, true} {
				if !streaming && prelude {
					continue
				}
				t.Run(fmt.Sprintf("%v/streaming=%t/prelude=%t", format, streaming, prelude), func(t *testing.T) {
					recorder := httptest.NewRecorder()
					caps := translate.NoticeCapabilities{Format: format, Presentation: translate.NoticeText, Streaming: streaming}
					delivery := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(recorder, caps), caps)
					if prelude {
						_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "visible prelude", "synthetic", ""))
						require.NoError(t, err)
					}
					require.NoError(t, delivery.DeliverFailure(syntheticTerminalFailure(), streaming))
					before := recorder.Body.String()
					require.NoError(t, delivery.DeliverFailure(syntheticTerminalFailure(), streaming))
					require.Equal(t, before, recorder.Body.String())
					if !streaming {
						require.Equal(t, http.StatusTooManyRequests, recorder.Code)
					}
					payload := recorder.Body.String()
					if streaming {
						events := splitSSEEvents(payload)
						payload = extractDataField(events[len(events)-1])
					}
					path := "error"
					if format == translate.FormatOpenAIResponses && streaming {
						path = "response.error"
						require.Equal(t, 1, strings.Count(before, "event: response.failed"))
					}
					require.Equal(t, "synthetic subscription pool exhausted", gjson.Get(payload, path+".message").Str)
					switch format {
					case translate.FormatAnthropic:
						require.Equal(t, "rate_limit_error", gjson.Get(payload, "error.type").Str)
					case translate.FormatGemini:
						require.EqualValues(t, 429, gjson.Get(payload, "error.code").Int())
						require.Equal(t, "RESOURCE_EXHAUSTED", gjson.Get(payload, "error.status").Str)
					default:
						require.Equal(t, "upstream_error", gjson.Get(payload, path+".code").Str)
					}
				})
			}
		}
	}
}

func TestNoticeFailureDropsTruncatedRecord(t *testing.T) {
	for _, format := range noticeFormats {
		recorder := httptest.NewRecorder()
		delivery := noticeDelivery(context.Background(), recorder, format)
		_, err := delivery.Write([]byte(`data: {"unterminated":"`))
		require.NoError(t, err)
		require.NoError(t, delivery.DeliverFailure(syntheticTerminalFailure(), true))
		require.NotContains(t, recorder.Body.String(), "unterminated")
		require.Contains(t, recorder.Body.String(), "synthetic subscription pool exhausted")
	}
}

func TestNoticeFailurePreservesExistingTerminalAndChatTrailers(t *testing.T) {
	for _, test := range []struct {
		format  translate.Format
		records string
	}{
		{translate.FormatAnthropic, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{translate.FormatGemini, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"},
		{translate.FormatOpenAIResponses, "data: {\"type\":\"response.failed\",\"sequence_number\":3,\"response\":{\"id\":\"native\",\"status\":\"failed\",\"error\":{\"message\":\"native failure\"},\"output\":[]}}\n\n"},
		{translate.FormatOpenAI, "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"total_tokens\":7}}\n\ndata: [DONE]\n\n"},
	} {
		recorder := httptest.NewRecorder()
		delivery := noticeDelivery(context.Background(), recorder, test.format)
		_, err := delivery.Write([]byte(test.records))
		require.NoError(t, err)
		require.NoError(t, delivery.DeliverFailure(syntheticTerminalFailure(), true))
		require.Equal(t, test.records, recorder.Body.String())
	}
}

func TestResponsesNoticeFailurePreservesPartialSnapshotAndSequence(t *testing.T) {
	recorder := httptest.NewRecorder()
	delivery := noticeDelivery(context.Background(), recorder, translate.FormatOpenAIResponses)
	_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "visible prelude", "synthetic", ""))
	require.NoError(t, err)
	_, err = delivery.Write([]byte("data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"provider\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"output_index\":0,\"item_id\":\"answer\",\"delta\":\"partial answer\"}\n\n"))
	require.NoError(t, err)
	failure := syntheticTerminalFailure()
	require.NoError(t, delivery.DeliverFailure(failure, true))
	events := splitSSEEvents(recorder.Body.String())
	previous := int64(-1)
	for _, event := range events {
		sequence := gjson.Get(extractDataField(event), "sequence_number").Int()
		require.Greater(t, sequence, previous)
		previous = sequence
	}
	terminal := gjson.Parse(extractDataField(events[len(events)-1]))
	output := terminal.Get("response.output").Array()
	require.Len(t, output, 2)
	require.Contains(t, output[0].Get("content.0.text").Str, "visible prelude")
	require.Equal(t, "partial answer", output[1].Get("content.0.text").Str)
	require.Equal(t, "answer", output[1].Get("id").Str)
	require.Equal(t, gjson.Get(extractDataField(events[0]), "response.id").Str, terminal.Get("response.id").Str)
}

func TestNoticeFailureCancellationDoesNotWrite(t *testing.T) {
	for _, format := range noticeFormats {
		ctx, cancel := context.WithCancel(context.Background())
		recorder := httptest.NewRecorder()
		delivery := noticeDelivery(ctx, recorder, format)
		cancel()
		require.ErrorIs(t, delivery.DeliverFailure(syntheticTerminalFailure(), true), context.Canceled)
		require.Empty(t, recorder.Body.String())
	}
}

func TestNoticeFailureAfterProviderOutput(t *testing.T) {
	for _, test := range []struct {
		format  translate.Format
		content string
	}{
		{translate.FormatAnthropic, "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"},
		{translate.FormatOpenAI, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"},
		{translate.FormatGemini, "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"},
		{translate.FormatOpenAIResponses, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"partial\"}\n\n"},
	} {
		recorder := httptest.NewRecorder()
		delivery := noticeDelivery(context.Background(), recorder, test.format)
		_, err := delivery.Write([]byte(test.content))
		require.NoError(t, err)
		require.NoError(t, delivery.DeliverFailure(syntheticTerminalFailure(), true))
		require.Contains(t, recorder.Body.String(), "partial")
		require.Contains(t, recorder.Body.String(), "synthetic subscription pool exhausted")
		before := recorder.Body.String()
		_, err = delivery.Write([]byte("data: [DONE]\n\n"))
		require.NoError(t, err)
		require.Equal(t, before, recorder.Body.String(), "no success is appended after failure")
	}
}

func TestNoticeFailureWaitsForEveryCandidate(t *testing.T) {
	recorder := httptest.NewRecorder()
	delivery := noticeDelivery(context.Background(), recorder, translate.FormatGemini)
	_, err := delivery.Write([]byte("data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"},{\"index\":1,\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"))
	require.NoError(t, err)
	require.NoError(t, delivery.DeliverFailure(syntheticTerminalFailure(), true))
	require.Contains(t, recorder.Body.String(), "synthetic subscription pool exhausted")
}

func TestNoticeFailureAfterRejectedResponsesTerminal(t *testing.T) {
	recorder := httptest.NewRecorder()
	delivery := noticeDelivery(context.Background(), recorder, translate.FormatOpenAIResponses)
	_, err := delivery.Deliver(translate.NewRouterNotice(syntheticNoticeKind, "visible prelude", "synthetic", ""))
	require.NoError(t, err)
	_, err = delivery.Write([]byte("data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"provider\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
	require.NoError(t, delivery.DeliverFailure(syntheticTerminalFailure(), true))
	require.Contains(t, recorder.Body.String(), "event: response.failed")
	require.NotContains(t, recorder.Body.String(), "event: response.completed")
}

func TestNoticeFailureAfterProvisionalTerminalAndMalformedTail(t *testing.T) {
	for _, fixture := range []struct {
		format translate.Format
		finish string
		tail   string
	}{
		{translate.FormatOpenAI, `data: {"choices":[{"index":0,"finish_reason":"stop"}]}`, `data: {"choices":[{"index":1`},
		{translate.FormatGemini, `data: {"candidates":[{"index":0,"finishReason":"STOP"}]}`, `data: {"candidates":[{"index":1`},
	} {
		for _, delimited := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/delimited=%t", fixture.format, delimited), func(t *testing.T) {
				recorder := httptest.NewRecorder()
				delivery := noticeDelivery(context.Background(), recorder, fixture.format)
				records := fixture.finish + "\n\n" + fixture.tail
				if delimited {
					records += "\n\n"
				}
				_, err := delivery.Write([]byte(records))
				require.NoError(t, err)
				require.NoError(t, delivery.Finalize())
				require.NoError(t, delivery.DeliverFailure(syntheticTerminalFailure(), true))
				require.Contains(t, recorder.Body.String(), fixture.finish)
				require.Equal(t, 1, strings.Count(recorder.Body.String(), "synthetic subscription pool exhausted"))
			})
		}
	}
}

func TestNoticeDeclaredFailureAbsorbsLaterOutcome(t *testing.T) {
	for _, format := range noticeFormats {
		for _, delimited := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/delimited=%t", format, delimited), func(t *testing.T) {
				recorder := httptest.NewRecorder()
				delivery := noticeDelivery(context.Background(), recorder, format)
				record := `event: error` + "\n" + `data: {"type":"error","error":{"message":"synthetic declared failure"}}`
				if delimited {
					record += "\n\n"
				}
				_, err := delivery.Write([]byte(record))
				require.NoError(t, err)
				require.NoError(t, delivery.DeliverFailure(syntheticTerminalFailure(), true))
				before := recorder.Body.String()
				require.Equal(t, 1, strings.Count(before, "synthetic declared failure"))
				require.NotContains(t, before, "synthetic subscription pool exhausted")
				_, err = delivery.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
				require.NoError(t, err)
				require.Equal(t, before, recorder.Body.String())
			})
		}
	}
}
