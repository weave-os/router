package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"

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
		protocol nativeStreamProtocol
		body     string
		want     error
	}{
		{name: "anthropic complete", protocol: nativeStreamAnthropic, body: anthropicStart + anthropicDelta + anthropicStop},
		{name: "anthropic final frame without trailing blank line", protocol: nativeStreamAnthropic, body: anthropicStart + strings.TrimSuffix(anthropicStop, "\n\n")},
		{name: "anthropic cut", protocol: nativeStreamAnthropic, body: anthropicStart + anthropicDelta, want: translate.ErrStreamIncomplete},
		{name: "anthropic never started", protocol: nativeStreamAnthropic, body: "event: ping\ndata: {\"type\":\"ping\"}\n\n", want: translate.ErrStreamEmpty},
		{name: "anthropic empty body", protocol: nativeStreamAnthropic, want: translate.ErrStreamEmpty},
		{name: "anthropic upstream error event", protocol: nativeStreamAnthropic, body: anthropicStart + "event: error\ndata: {\"type\":\"error\"}\n\n"},
		{name: "unframed json body", protocol: nativeStreamAnthropic, body: `{"type":"message","content":[]}`},
		{name: "anthropic keepalives only", protocol: nativeStreamAnthropic, body: ": keepalive\n\n: keepalive\n\n", want: translate.ErrStreamEmpty},
		{name: "anthropic truncated final frame", protocol: nativeStreamAnthropic, body: anthropicStart + "event: message_stop\ndata: {\"type\":\"mess", want: translate.ErrStreamIncomplete},
		{name: "chat finish reason", protocol: nativeStreamOpenAIChat, body: chatDelta + chatFinish},
		{name: "chat done sentinel", protocol: nativeStreamOpenAIChat, body: chatDelta + "data: [DONE]\n\n"},
		{name: "chat cut", protocol: nativeStreamOpenAIChat, body: chatDelta, want: translate.ErrStreamIncomplete},
		{name: "chat error object", protocol: nativeStreamOpenAIChat, body: chatDelta + `data: {"error":{"message":"overloaded"}}` + "\n\n"},
		{name: "chat empty body", protocol: nativeStreamOpenAIChat, want: translate.ErrStreamEmpty},
		{name: "chat null error field is not an end", protocol: nativeStreamOpenAIChat, body: `data: {"error":null,"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}` + "\n\n", want: translate.ErrStreamIncomplete},
		{name: "chat keepalives only", protocol: nativeStreamOpenAIChat, body: ": OPENROUTER PROCESSING\n\n", want: translate.ErrStreamEmpty},
		{name: "chat second choice still generating", protocol: nativeStreamOpenAIChat, body: chatDelta + chatSecondChoiceDelta + chatFinish, want: translate.ErrStreamIncomplete},
		{name: "chat every choice finished", protocol: nativeStreamOpenAIChat, body: chatDelta + chatSecondChoiceDelta + chatFinish + chatSecondChoiceFinish},
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
	o := newStreamTerminalObserver(httptest.NewRecorder(), nativeStreamAnthropic)
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
