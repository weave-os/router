package proxy

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamTerminalObserver(t *testing.T) {
	const (
		anthropicStart = "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
		anthropicDelta = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\"}\n\n"
		anthropicStop  = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		chatDelta      = `data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}` + "\n\n"
		chatFinish     = `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"

		chatSecondChoiceDelta  = `data: {"choices":[{"index":1,"delta":{"content":"b"},"finish_reason":null}]}` + "\n\n"
		chatSecondChoiceFinish = `data: {"choices":[{"index":1,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	)
	cases := []struct {
		name     string
		protocol translate.Format
		body     string
		want     error
	}{
		{name: "anthropic complete", protocol: translate.FormatAnthropic, body: anthropicStart + anthropicDelta + anthropicStop},
		{name: "anthropic final frame without trailing blank line", protocol: translate.FormatAnthropic, body: anthropicStart + strings.TrimSuffix(anthropicStop, "\n\n")},
		{name: "anthropic cut", protocol: translate.FormatAnthropic, body: anthropicStart + anthropicDelta, want: translate.ErrStreamIncomplete},
		{name: "anthropic never started", protocol: translate.FormatAnthropic, body: "event: ping\ndata: {\"type\":\"ping\"}\n\n", want: translate.ErrStreamEmpty},
		{name: "anthropic empty body", protocol: translate.FormatAnthropic, want: translate.ErrStreamEmpty},
		{name: "anthropic upstream error event", protocol: translate.FormatAnthropic, body: anthropicStart + "event: error\ndata: {\"type\":\"error\"}\n\n"},
		{name: "unframed json body", protocol: translate.FormatAnthropic, body: `{"type":"message","content":[]}`},
		{name: "anthropic keepalives only", protocol: translate.FormatAnthropic, body: ": keepalive\n\n: keepalive\n\n", want: translate.ErrStreamEmpty},
		{name: "anthropic truncated final frame", protocol: translate.FormatAnthropic, body: anthropicStart + "event: message_stop\ndata: {\"type\":\"mess", want: translate.ErrStreamIncomplete},
		{name: "chat finish reason", protocol: translate.FormatOpenAI, body: chatDelta + chatFinish},
		{name: "chat done sentinel", protocol: translate.FormatOpenAI, body: chatDelta + "data: [DONE]\n\n"},
		{name: "chat cut", protocol: translate.FormatOpenAI, body: chatDelta, want: translate.ErrStreamIncomplete},
		{name: "chat error object", protocol: translate.FormatOpenAI, body: chatDelta + `data: {"error":{"message":"overloaded"}}` + "\n\n"},
		{name: "chat empty body", protocol: translate.FormatOpenAI, want: translate.ErrStreamEmpty},
		{name: "chat null error field is not an end", protocol: translate.FormatOpenAI, body: `data: {"error":null,"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}` + "\n\n", want: translate.ErrStreamIncomplete},
		{name: "chat keepalives only", protocol: translate.FormatOpenAI, body: ": OPENROUTER PROCESSING\n\n", want: translate.ErrStreamEmpty},
		{name: "chat second choice still generating", protocol: translate.FormatOpenAI, body: chatDelta + chatSecondChoiceDelta + chatFinish, want: translate.ErrStreamIncomplete},
		{name: "chat every choice finished", protocol: translate.FormatOpenAI, body: chatDelta + chatSecondChoiceDelta + chatFinish + chatSecondChoiceFinish},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			o := newStreamTerminalObserver(rec, tc.protocol)
			// One byte per write: frames arrive split across reads upstream.
			for i := range len(tc.body) {
				_, err := o.Write([]byte{tc.body[i]})
				require.NoError(t, err)
			}
			assert.Equal(t, tc.body, rec.Body.String(), "the observer tees bytes unchanged")
			if tc.want == nil {
				assert.NoError(t, o.streamErr())
				return
			}
			assert.ErrorIs(t, o.streamErr(), tc.want)
		})
	}
}

// An oversized frame is dropped rather than retained, and the observer
// resyncs on the next delimiter.
func TestStreamTerminalObserverBoundsPartialFrame(t *testing.T) {
	o := newStreamTerminalObserver(httptest.NewRecorder(), translate.FormatAnthropic)
	_, err := o.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: " + strings.Repeat("x", streamCutCarryCap+1)))
	require.NoError(t, err)
	assert.LessOrEqual(t, o.buf.Len(), streamCutCarryCap)

	_, err = o.Write([]byte("\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	require.NoError(t, err)
	assert.NoError(t, o.streamErr())
}

func TestResponsesTerminalObserverState(t *testing.T) {
	const created = `data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}` + "\n\n"
	cases := []struct {
		name string
		body string
		want error
	}{
		{name: "completed", body: created + `data: {"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n"},
		{name: "failed terminal names its outcome", body: created + `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error"}}}` + "\n\n"},
		{name: "cut after created", body: created, want: translate.ErrStreamIncomplete},
		{name: "keepalive only", body: ": keepalive\n\n", want: translate.ErrStreamEmpty},
		{name: "terminal named only by the event field", body: created + "event: response.completed\ndata: {\"response\":{\"status\":\"completed\"}}\n\n"},
		{name: "truncated final frame", body: created + `data: {"type":"response.completed","response":{"sta`, want: translate.ErrStreamIncomplete},
		{name: "empty", want: translate.ErrStreamEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newResponsesTerminalObserver(httptest.NewRecorder())
			_, err := o.Write([]byte(tc.body))
			require.NoError(t, err)
			o.Finalize()
			if tc.want == nil {
				assert.NoError(t, o.state.err())
				return
			}
			assert.ErrorIs(t, o.state.err(), tc.want)
		})
	}
}

func TestNativeStreamTerminalObserverRejectsMalformedTerminalRecords(t *testing.T) {
	for _, body := range []string{
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"\n\n",
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_stop\ndata: broken\n\n",
	} {
		o := newStreamTerminalObserver(httptest.NewRecorder(), translate.FormatAnthropic)
		_, err := o.Write([]byte(body))
		require.NoError(t, err)
		require.ErrorIs(t, o.streamErr(), translate.ErrStreamIncomplete)
	}
}

func TestNativeStreamTerminalObserverRejectsInvalidUnframedJSON(t *testing.T) {
	o := newStreamTerminalObserver(httptest.NewRecorder(), translate.FormatAnthropic)
	_, err := o.Write([]byte(`{"type":"message","content":[`))
	require.NoError(t, err)
	require.Error(t, o.streamErr(), "a truncated body cannot count as a successful non-SSE fallback")
}

func TestStreamTerminalObserverGemini(t *testing.T) {
	const content = `data: {"candidates":[{"index":0,"content":{"parts":[{"text":"partial"}]}}]}` + "\n\n"
	const terminal = `data: {"candidates":[{"index":0,"finishReason":"STOP"}]}`
	for _, fixture := range []struct {
		name string
		body string
		want error
	}{
		{name: "missing finish", body: content, want: translate.ErrStreamIncomplete},
		{name: "complete undelimited terminal", body: content + terminal},
		{name: "complete delimited terminal", body: content + terminal + "\n\n"},
		{name: "comment only", body: ": keepalive\n\n", want: translate.ErrStreamEmpty},
		{name: "truncated terminal", body: content + strings.TrimSuffix(terminal, "]}"), want: translate.ErrStreamIncomplete},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			o := newStreamTerminalObserver(rec, translate.FormatGemini)
			for _, chunk := range []byte(fixture.body) {
				_, err := o.Write([]byte{chunk})
				require.NoError(t, err)
			}
			if fixture.want != nil {
				require.ErrorIs(t, o.OutcomeError(), fixture.want)
			} else {
				require.NoError(t, o.OutcomeError())
			}
			assert.Equal(t, fixture.body, rec.Body.String())
		})
	}
}

func TestNativeTerminalObserversCancellation(t *testing.T) {
	type attemptObserver interface {
		io.Writer
		Cancel(error)
		OutcomeError() error
	}
	for _, fixture := range []struct {
		name        string
		newObserver func() attemptObserver
		partial     string
		terminal    string
	}{
		{
			name: "anthropic",
			newObserver: func() attemptObserver {
				return newStreamTerminalObserver(httptest.NewRecorder(), translate.FormatAnthropic)
			},
			partial:  `data: {"type":"message_start"}` + "\n\n",
			terminal: `data: {"type":"message_stop"}`,
		},
		{
			name: "chat",
			newObserver: func() attemptObserver {
				return newStreamTerminalObserver(httptest.NewRecorder(), translate.FormatOpenAI)
			},
			partial:  `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n",
			terminal: `data: [DONE]`,
		},
		{
			name: "gemini",
			newObserver: func() attemptObserver {
				return newStreamTerminalObserver(httptest.NewRecorder(), translate.FormatGemini)
			},
			partial:  `data: {"candidates":[{"index":0,"content":{"parts":[{"text":"partial"}]}}]}` + "\n\n",
			terminal: `data: {"candidates":[{"index":0,"finishReason":"STOP"}]}`,
		},
		{
			name:        "responses",
			newObserver: func() attemptObserver { return newResponsesTerminalObserver(httptest.NewRecorder()) },
			partial:     `data: {"type":"response.created","response":{"status":"in_progress"}}` + "\n\n",
			terminal:    `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			for _, prefix := range []string{"", ": keepalive\n\n", fixture.partial, fixture.partial + `data: {"truncated"`} {
				observer := fixture.newObserver()
				_, err := observer.Write([]byte(prefix))
				require.NoError(t, err)
				observer.Cancel(context.Canceled)
				require.ErrorIs(t, observer.OutcomeError(), context.Canceled)
			}
			observer := fixture.newObserver()
			_, err := observer.Write([]byte(fixture.partial + fixture.terminal))
			require.NoError(t, err)
			observer.Cancel(context.Canceled)
			require.NoError(t, observer.OutcomeError(), "a complete undelimited terminal preceded cancellation")

			observer = fixture.newObserver()
			_, err = observer.Write([]byte(`data: {"error":{"message":"synthetic provider failure"}}`))
			require.NoError(t, err)
			observer.Cancel(context.Canceled)
			var failure *providers.UpstreamErrorResponse
			require.ErrorAs(t, observer.OutcomeError(), &failure)
			assert.JSONEq(t, `{"error":{"message":"synthetic provider failure"}}`, string(failure.Body))
			require.NotErrorIs(t, observer.OutcomeError(), context.Canceled)
		})
	}
}

func TestNativeBufferedDeclaredErrorPreservesBodyCompatibility(t *testing.T) {
	type bufferedObserver interface {
		io.Writer
		DeclaredError() error
	}
	for _, format := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses} {
		for _, body := range []string{"", `{"synthetic":"unframed response"}`} {
			recorder := httptest.NewRecorder()
			var observer bufferedObserver
			if format == translate.FormatOpenAIResponses {
				observer = newResponsesTerminalObserver(recorder)
			} else {
				observer = newStreamTerminalObserver(recorder, format)
			}
			_, err := observer.Write([]byte(body))
			require.NoError(t, err)
			require.NoError(t, observer.DeclaredError())
			require.Equal(t, body, recorder.Body.String())
		}
	}
}

func TestStreamTerminalObserverLargeBufferedJSON(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		format translate.Format
		body   string
		failed bool
	}{
		{
			name: "gemini completed", format: translate.FormatGemini,
			body: `{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("x", streamCutCarryCap+1) + `"}]},"finishReason":"STOP"}]}`,
		},
		{
			name: "anthropic declared failure", format: translate.FormatAnthropic, failed: true,
			body: `{"type":"error","error":{"type":"api_error","message":"` + strings.Repeat("x", streamCutCarryCap+1) + `"}}`,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json; charset=utf-8")
			observer := newStreamTerminalObserver(recorder, fixture.format)
			for offset := 0; offset < len(fixture.body); offset += 4096 {
				_, err := observer.Write([]byte(fixture.body[offset:min(offset+4096, len(fixture.body))]))
				require.NoError(t, err)
			}
			if fixture.failed {
				var failure *providers.UpstreamErrorResponse
				require.ErrorAs(t, observer.DeclaredError(), &failure)
				require.JSONEq(t, fixture.body, string(failure.Body))
			} else {
				require.NoError(t, observer.OutcomeError())
			}
			require.Equal(t, fixture.body, recorder.Body.String())
		})
	}
}
