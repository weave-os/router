package translate_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/require"
)

type failureTestWriter interface {
	http.ResponseWriter
	Finalize() error
}

func retainedFailure(writer failureTestWriter, writeErr, finalErr error) error {
	if observer, ok := writer.(interface{ UpstreamError() error }); ok {
		return errors.Join(writeErr, finalErr, observer.UpstreamError())
	}
	return errors.Join(writeErr, finalErr)
}

func TestTranslatorsRejectTruncatedTerminalJSON(t *testing.T) {
	for _, test := range []struct {
		name      string
		newWriter func(http.ResponseWriter) failureTestWriter
		content   string
		terminal  string
		success   string
	}{
		{"anthropic to chat", func(w http.ResponseWriter) failureTestWriter { return translate.NewSSETranslator(w, "synthetic", nil) }, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n", "event: message_stop\ndata: {\"type\":\"message_stop\"", "data: [DONE]"},
		{"chat to anthropic", func(w http.ResponseWriter) failureTestWriter {
			return translate.NewAnthropicSSETranslator(w, "synthetic", nil)
		}, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n", "data: {\"choices\":[{\"finish_reason\":\"stop\"}", "event: message_stop"},
		{"gemini to chat", func(w http.ResponseWriter) failureTestWriter {
			return translate.NewGeminiToOpenAISSETranslator(w, "synthetic", nil)
		}, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"answer\"}]}}]}\n\n", "data: {\"candidates\":[{\"finishReason\":\"STOP\"}", "data: [DONE]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writer := test.newWriter(recorder)
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			_, err := writer.Write([]byte(test.content))
			require.NoError(t, err)
			_, writeErr := writer.Write([]byte(test.terminal))
			finalErr := writer.Finalize()
			require.Error(t, retainedFailure(writer, writeErr, finalErr))
			require.NotContains(t, recorder.Body.String(), test.success)
		})
	}
}

func TestResponsesAnthropicRetainsDeclaredFailureAfterOutput(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := translate.NewResponsesToAnthropicWriter(recorder, "synthetic", nil)
	require.NoError(t, writer.Prelude(true))
	_, err := writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"answer\"}\n\n"))
	require.NoError(t, err)
	_, writeErr := writer.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"synthetic context overflow\"}}}\n\n"))
	finalErr := writer.Finalize()
	var upstream *providers.UpstreamErrorResponse
	require.ErrorAs(t, retainedFailure(writer, writeErr, finalErr), &upstream)
	require.Equal(t, http.StatusBadRequest, upstream.Status)
	require.Contains(t, string(upstream.Body), "context_length_exceeded")
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: error"))
	require.NotContains(t, recorder.Body.String(), "event: message_stop")
}

type ownedFailureTestWriter interface {
	failureTestWriter
	SetFailureOwner()
	UpstreamError() error
}

func ownedTranslatorCases() []struct {
	name      string
	newWriter func(http.ResponseWriter) ownedFailureTestWriter
	content   string
	failure   string
} {
	return []struct {
		name      string
		newWriter func(http.ResponseWriter) ownedFailureTestWriter
		content   string
		failure   string
	}{
		{"anthropic to chat", func(w http.ResponseWriter) ownedFailureTestWriter {
			return translate.NewSSETranslator(w, "synthetic", nil)
		}, "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"synthetic context overflow\"}}\n\n"},
		{"chat to anthropic", func(w http.ResponseWriter) ownedFailureTestWriter {
			return translate.NewAnthropicSSETranslator(w, "synthetic", nil)
		}, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n", "data: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"synthetic context overflow\"}}\n\n"},
		{"gemini to chat", func(w http.ResponseWriter) ownedFailureTestWriter {
			return translate.NewGeminiToOpenAISSETranslator(w, "synthetic", nil)
		}, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"answer\"}]}}]}\n\n", "data: {\"error\":{\"code\":400,\"status\":\"INVALID_ARGUMENT\",\"message\":\"synthetic context overflow\"}}\n\n"},
		{"responses to anthropic", func(w http.ResponseWriter) ownedFailureTestWriter {
			return translate.NewResponsesToAnthropicWriter(w, "synthetic", nil)
		}, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"answer\"}\n\n", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"synthetic context overflow\"}}}\n\n"},
		{"responses to chat", func(w http.ResponseWriter) ownedFailureTestWriter {
			return translate.NewResponsesToOpenAIChatWriter(w, "synthetic", nil)
		}, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"answer\"}\n\n", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"synthetic context overflow\"}}}\n\n"},
	}
}

func startOwnedTranslator(t *testing.T, writer ownedFailureTestWriter) {
	t.Helper()
	writer.SetFailureOwner()
	if prelude, ok := writer.(interface{ Prelude(bool) error }); ok {
		require.NoError(t, prelude.Prelude(true))
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
}

func TestTranslatorsLeaveTerminalFailureToOwner(t *testing.T) {
	for _, test := range ownedTranslatorCases() {
		for _, output := range []bool{false, true} {
			t.Run(test.name+"/output="+map[bool]string{false: "false", true: "true"}[output], func(t *testing.T) {
				recorder := httptest.NewRecorder()
				writer := test.newWriter(recorder)
				startOwnedTranslator(t, writer)
				if output {
					_, err := writer.Write([]byte(test.content))
					require.NoError(t, err)
				}
				beforeFailure := recorder.Body.String()
				// Fragmented provider errors must retain their original classified body.
				midpoint := len(test.failure) / 2
				_, err := writer.Write([]byte(test.failure[:midpoint]))
				require.NoError(t, err)
				_, writeErr := writer.Write([]byte(test.failure[midpoint:]))
				var upstream *providers.UpstreamErrorResponse
				require.ErrorAs(t, retainedFailure(writer, writeErr, writer.Finalize()), &upstream)
				require.Equal(t, http.StatusBadRequest, upstream.Status)
				require.Contains(t, string(upstream.Body), "synthetic context overflow")
				require.Equal(t, beforeFailure, recorder.Body.String())
			})
		}
	}
}

func TestTranslatorsLeaveMissingTerminalToOwner(t *testing.T) {
	for _, test := range ownedTranslatorCases() {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writer := test.newWriter(recorder)
			startOwnedTranslator(t, writer)
			_, err := writer.Write([]byte(test.content))
			require.NoError(t, err)
			beforeFailure := recorder.Body.String()
			require.ErrorIs(t, writer.Finalize(), translate.ErrStreamIncomplete)
			require.Equal(t, beforeFailure, recorder.Body.String())
		})
	}
}

func TestTranslatorsLeaveBufferedErrorsToOwner(t *testing.T) {
	for _, test := range ownedTranslatorCases() {
		for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
			t.Run(test.name+"/"+http.StatusText(status), func(t *testing.T) {
				recorder := httptest.NewRecorder()
				writer := test.newWriter(recorder)
				writer.SetFailureOwner()
				writer.WriteHeader(status)
				body := `{"error":{"code":"context_length_exceeded","message":"synthetic context overflow"}}`
				if status >= 400 {
					body = `{"error":{"code":"invalid_api_key","message":"synthetic authentication failure"}}`
				}
				_, err := writer.Write([]byte(body))
				require.NoError(t, err)
				var upstream *providers.UpstreamErrorResponse
				require.ErrorAs(t, writer.Finalize(), &upstream)
				if status >= 400 {
					require.Equal(t, status, upstream.Status)
					require.Equal(t, body, string(upstream.Body))
				} else {
					require.Equal(t, http.StatusBadRequest, upstream.Status)
				}
				require.Empty(t, recorder.Body.String())
			})
		}
	}
}

func TestTranslatedStreamsWaitForEveryObservedChoice(t *testing.T) {
	for _, test := range []struct {
		name              string
		newWriter         func(http.ResponseWriter) ownedFailureTestWriter
		partial           string
		remainingTerminal string
		success           string
	}{
		{
			name: "Gemini candidates",
			newWriter: func(w http.ResponseWriter) ownedFailureTestWriter {
				return translate.NewGeminiToOpenAISSETranslator(w, "synthetic", nil)
			},
			partial:           `data: {"candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"},{"index":1,"content":{"parts":[{"text":"other answer"}]}}]}` + "\n\n",
			remainingTerminal: `data: {"candidates":[{"index":1,"finishReason":"STOP"}]}` + "\n\n",
			success:           "data: [DONE]",
		},
		{
			name: "Chat choices",
			newWriter: func(w http.ResponseWriter) ownedFailureTestWriter {
				return translate.NewAnthropicSSETranslator(w, "synthetic", nil)
			},
			partial:           `data: {"choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"},{"index":1,"delta":{"content":"other answer"},"finish_reason":null}]}` + "\n\n",
			remainingTerminal: `data: {"choices":[{"index":1,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
			success:           "event: message_stop",
		},
	} {
		for _, complete := range []bool{false, true} {
			t.Run(test.name+"/complete="+map[bool]string{false: "false", true: "true"}[complete], func(t *testing.T) {
				recorder := httptest.NewRecorder()
				writer := test.newWriter(recorder)
				startOwnedTranslator(t, writer)
				_, err := writer.Write([]byte(test.partial))
				require.NoError(t, err)
				require.NotContains(t, recorder.Body.String(), test.success)
				if complete {
					_, err = writer.Write([]byte(test.remainingTerminal))
					require.NoError(t, err)
					require.NoError(t, writer.Finalize())
					require.Equal(t, 1, strings.Count(recorder.Body.String(), test.success))
				} else {
					require.ErrorIs(t, writer.Finalize(), translate.ErrStreamIncomplete)
					require.NotContains(t, recorder.Body.String(), test.success)
				}
			})
		}
	}
}

func TestTranslatedChatRejectsDoneWithUnfinishedChoice(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := translate.NewAnthropicSSETranslator(recorder, "synthetic", nil)
	startOwnedTranslator(t, writer)
	_, err := writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{},\"finish_reason\":null}]}\n\n"))
	require.NoError(t, err)
	_, writeErr := writer.Write([]byte("data: [DONE]\n\n"))
	require.ErrorIs(t, retainedFailure(writer, writeErr, writer.Finalize()), translate.ErrStreamIncomplete)
	require.NotContains(t, recorder.Body.String(), "event: message_stop")
}

func TestBufferedGeminiTranslatorRequiresTerminalFinish(t *testing.T) {
	for _, body := range []string{
		`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`,
		`{"candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"},{"index":1,"content":{"parts":[{"text":"partial"}]}}]}`,
	} {
		recorder := httptest.NewRecorder()
		writer := translate.NewGeminiToOpenAISSETranslator(recorder, "synthetic", nil)
		writer.SetFailureOwner()
		writer.WriteHeader(http.StatusOK)
		_, err := writer.Write([]byte(body))
		require.NoError(t, err)
		require.ErrorIs(t, writer.Finalize(), translate.ErrStreamIncomplete)
		require.Empty(t, recorder.Body.String())
	}
}

func TestOwnerTranslatorsValidateAfterChoiceFinish(t *testing.T) {
	for _, test := range []struct {
		name       string
		newWriter  func(http.ResponseWriter) ownedFailureTestWriter
		finished   string
		unfinished string
		completed  string
		truncated  string
		failure    string
		success    string
	}{
		{
			name: "Gemini",
			newWriter: func(w http.ResponseWriter) ownedFailureTestWriter {
				return translate.NewGeminiToOpenAISSETranslator(w, "synthetic", nil)
			},
			finished:   `data: {"candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}` + "\n\n",
			unfinished: `data: {"candidates":[{"index":1,"content":{"parts":[{"text":"second answer"}]}}]}` + "\n\n",
			completed:  `data: {"candidates":[{"index":1,"content":{"parts":[{"text":"second answer"}]},"finishReason":"STOP"}]}` + "\n\n",
			truncated:  `data: {"candidates":[{"index":1`,
			failure:    `data: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"synthetic provider failure"}}` + "\n\n",
			success:    "data: [DONE]",
		},
		{
			name: "Chat",
			newWriter: func(w http.ResponseWriter) ownedFailureTestWriter {
				return translate.NewAnthropicSSETranslator(w, "synthetic", nil)
			},
			finished:   `data: {"choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}` + "\n\n",
			unfinished: `data: {"choices":[{"index":1,"delta":{"content":"second answer"},"finish_reason":null}]}` + "\n\n",
			completed:  `data: {"choices":[{"index":1,"delta":{"content":"second answer"},"finish_reason":"stop"}]}` + "\n\n",
			truncated:  `data: {"choices":[{"index":1`,
			failure:    `data: {"error":{"type":"rate_limit_error","message":"synthetic provider failure"}}` + "\n\n",
			success:    "event: message_stop",
		},
	} {
		for _, tail := range []struct {
			name, body string
			complete   bool
		}{
			{"new unfinished choice", test.unfinished, false},
			{"truncated record", test.truncated, false},
			{"provider failure", test.failure, false},
			{"new complete choice", test.completed, true},
		} {
			t.Run(test.name+"/"+tail.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				writer := test.newWriter(recorder)
				startOwnedTranslator(t, writer)
				_, err := writer.Write([]byte(test.finished))
				require.NoError(t, err)
				require.NotContains(t, recorder.Body.String(), test.success)
				_, writeErr := writer.Write([]byte(tail.body))
				outcome := retainedFailure(writer, writeErr, writer.Finalize())
				if tail.complete {
					require.NoError(t, outcome)
					require.Equal(t, 1, strings.Count(recorder.Body.String(), test.success))
				} else {
					require.Error(t, outcome)
					require.NotContains(t, recorder.Body.String(), test.success)
				}
			})
		}
	}
}

func TestResponsesTranslatorsRetainEmptyCompletionFailure(t *testing.T) {
	for _, newWriter := range []func(http.ResponseWriter) ownedFailureTestWriter{
		func(w http.ResponseWriter) ownedFailureTestWriter {
			return translate.NewResponsesToAnthropicWriter(w, "synthetic", nil)
		},
		func(w http.ResponseWriter) ownedFailureTestWriter {
			return translate.NewResponsesToOpenAIChatWriter(w, "synthetic", nil)
		},
	} {
		recorder := httptest.NewRecorder()
		writer := newWriter(recorder)
		startOwnedTranslator(t, writer)
		_, err := writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"))
		require.Error(t, err)
		require.Error(t, writer.UpstreamError())
		require.Error(t, writer.Finalize())
		require.NotContains(t, recorder.Body.String(), "event: message_stop")
		require.NotContains(t, recorder.Body.String(), "data: [DONE]")
	}
}
