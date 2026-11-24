package dispatch

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
		protocol NativeStreamProtocol
		body     string
		want     error
	}{
		{name: "anthropic complete", protocol: NativeStreamAnthropic, body: anthropicStart + anthropicDelta + anthropicStop},
		{name: "anthropic final frame without trailing blank line", protocol: NativeStreamAnthropic, body: anthropicStart + strings.TrimSuffix(anthropicStop, "\n\n")},
		{name: "anthropic cut", protocol: NativeStreamAnthropic, body: anthropicStart + anthropicDelta, want: translate.ErrStreamIncomplete},
		{name: "anthropic never started", protocol: NativeStreamAnthropic, body: "event: ping\ndata: {\"type\":\"ping\"}\n\n", want: translate.ErrStreamEmpty},
		{name: "anthropic empty body", protocol: NativeStreamAnthropic, want: translate.ErrStreamEmpty},
		{name: "anthropic upstream error event", protocol: NativeStreamAnthropic, body: anthropicStart + "event: error\ndata: {\"type\":\"error\"}\n\n"},
		{name: "unframed json body", protocol: NativeStreamAnthropic, body: `{"type":"message","content":[]}`},
		{name: "anthropic keepalives only", protocol: NativeStreamAnthropic, body: ": keepalive\n\n: keepalive\n\n", want: translate.ErrStreamEmpty},
		{name: "anthropic truncated final frame", protocol: NativeStreamAnthropic, body: anthropicStart + "event: message_stop\ndata: {\"type\":\"mess", want: translate.ErrStreamIncomplete},
		{name: "chat finish reason", protocol: NativeStreamOpenAIChat, body: chatDelta + chatFinish},
		{name: "chat done sentinel", protocol: NativeStreamOpenAIChat, body: chatDelta + "data: [DONE]\n\n"},
		{name: "chat cut", protocol: NativeStreamOpenAIChat, body: chatDelta, want: translate.ErrStreamIncomplete},
		{name: "chat error object", protocol: NativeStreamOpenAIChat, body: chatDelta + `data: {"error":{"message":"overloaded"}}` + "\n\n"},
		{name: "chat empty body", protocol: NativeStreamOpenAIChat, want: translate.ErrStreamEmpty},
		{name: "chat null error field is not an end", protocol: NativeStreamOpenAIChat, body: `data: {"error":null,"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}` + "\n\n", want: translate.ErrStreamIncomplete},
		{name: "chat keepalives only", protocol: NativeStreamOpenAIChat, body: ": OPENROUTER PROCESSING\n\n", want: translate.ErrStreamEmpty},
		{name: "chat second choice still generating", protocol: NativeStreamOpenAIChat, body: chatDelta + chatSecondChoiceDelta + chatFinish, want: translate.ErrStreamIncomplete},
		{name: "chat every choice finished", protocol: NativeStreamOpenAIChat, body: chatDelta + chatSecondChoiceDelta + chatFinish + chatSecondChoiceFinish},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			o := NewStreamTerminalObserver(rec, tc.protocol)
			// One byte per write: frames arrive split across reads upstream.
			for i := range len(tc.body) {
				_, err := o.Write([]byte{tc.body[i]})
				require.NoError(t, err)
			}
			assert.Equal(t, tc.body, rec.Body.String(), "the observer tees bytes unchanged")
			if tc.want == nil {
				assert.NoError(t, o.StreamErr())
				return
			}
			assert.ErrorIs(t, o.StreamErr(), tc.want)
		})
	}
}

// An oversized frame is dropped rather than retained, and the observer
// resyncs on the next delimiter.
func TestStreamTerminalObserverBoundsPartialFrame(t *testing.T) {
	o := NewStreamTerminalObserver(httptest.NewRecorder(), NativeStreamAnthropic)
	_, err := o.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: " + strings.Repeat("x", streamCutCarryCap+1)))
	require.NoError(t, err)
	assert.LessOrEqual(t, o.buf.Len(), streamCutCarryCap)

	_, err = o.Write([]byte("\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	require.NoError(t, err)
	assert.NoError(t, o.StreamErr())
}
