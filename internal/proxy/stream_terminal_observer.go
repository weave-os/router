package proxy

import (
	"bytes"
	"errors"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

// nativeStreamProtocol names the wire format a native passthrough stream
// speaks, which decides what counts as its start and its terminal event.
type nativeStreamProtocol uint8

const (
	nativeStreamAnthropic nativeStreamProtocol = iota
	nativeStreamOpenAIChat
)

const (
	anthropicMessageStartEvent = "message_start"
	anthropicMessageStopEvent  = "message_stop"
	openAIChatDoneSentinel     = "[DONE]"
)

// streamTerminalState records whether a native passthrough stream began and
// whether the upstream declared its end. The adapters return nil on a clean
// EOF after a 2xx, so without it a connection closed mid-turn reads as a
// served turn. An upstream error event counts as declared: the upstream named
// the failure on the wire itself.
type streamTerminalState struct {
	// wrote and framed tell an SSE stream apart from a body the upstream sent
	// unframed (a JSON answer to a streaming request), which has no terminal
	// event to miss.
	wrote   bool
	framed  bool
	started bool
	ended   bool
}

// A comment-only frame (a keepalive) is SSE too: a stream of nothing else
// never started rather than being an unframed body.
func (s *streamTerminalState) noteFrame(event, eventType, payload []byte) {
	s.framed = s.framed || len(eventType) > 0 || len(payload) > 0 ||
		bytes.HasPrefix(bytes.TrimLeft(event, "\r\n"), []byte(":"))
}

// completeTailPayload reports whether a final frame that arrived without its
// blank-line delimiter carries a whole payload; a truncated one must not count
// as the terminal it was cut from.
func completeTailPayload(payload []byte) bool {
	return string(bytes.TrimSpace(payload)) == openAIChatDoneSentinel || gjson.ValidBytes(payload)
}

// err reports how the stream ended: nil once a terminal arrived or when the
// body was not SSE, translate.ErrStreamEmpty when the upstream never started
// the response, and translate.ErrStreamIncomplete when it started but never
// finished.
func (s streamTerminalState) err() error {
	switch {
	case s.ended, s.wrote && !s.framed:
		return nil
	case !s.started:
		return translate.ErrStreamEmpty
	default:
		return translate.ErrStreamIncomplete
	}
}

// streamTerminalObserver tees a native Anthropic or OpenAI chat stream to
// inner unchanged while tracking its streamTerminalState. Native Responses
// streams are tracked by responsesTerminalObserver, which already parses
// their terminal events.
type streamTerminalObserver struct {
	inner    http.ResponseWriter
	protocol nativeStreamProtocol
	// buf holds the bytes of the frame currently being assembled; SSE frames
	// arrive split across writes.
	buf     bytes.Buffer
	framing sse.Scanner
	state   streamTerminalState
	// openChoices are chat choice indexes that emitted without finishing yet;
	// with n > 1 one choice can finish while another is still generating.
	openChoices     map[int64]struct{}
	finishedChoices map[int64]struct{}
}

func newStreamTerminalObserver(inner http.ResponseWriter, protocol nativeStreamProtocol) *streamTerminalObserver {
	return &streamTerminalObserver{inner: inner, protocol: protocol}
}

func (o *streamTerminalObserver) Header() http.Header { return o.inner.Header() }

func (o *streamTerminalObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *streamTerminalObserver) Write(p []byte) (int, error) {
	o.state.wrote = o.state.wrote || len(p) > 0
	o.buf.Write(p)
	for {
		event, n := o.framing.Next(o.buf.Bytes())
		if n == 0 {
			break
		}
		o.observeEvent(event)
		o.buf.Next(n)
	}
	// A terminal frame is small; resyncing on the next delimiter bounds memory
	// for an oversized frame or an unframed body without losing one.
	if o.buf.Len() > streamCutCarryCap {
		o.buf.Reset()
		o.framing.Reset()
	}
	return o.inner.Write(p)
}

func (o *streamTerminalObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress forwards the output-stall watchdog hook; hiding it from
// the provider client would downgrade the stream to byte-idle guarding.
func (o *streamTerminalObserver) ArmOutputProgress(mark func()) bool {
	arm, ok := o.inner.(providers.OutputProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// ArmReasoningProgress forwards the reasoning-progress hook for the same reason.
func (o *streamTerminalObserver) ArmReasoningProgress(mark func()) bool {
	arm, ok := o.inner.(providers.ReasoningProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmReasoningProgress(mark)
}

// streamErr reports the stream's ending once the upstream call has returned,
// first reading a final frame that arrived without a trailing blank line.
func (o *streamTerminalObserver) streamErr() error {
	if o.buf.Len() > 0 {
		rest := o.buf.Bytes()
		o.buf.Reset()
		o.framing.Reset()
		eventType, payload := sse.ParseEvent(rest)
		if completeTailPayload(payload) {
			o.observeEvent(rest)
		} else {
			o.state.noteFrame(rest, eventType, payload)
		}
	}
	if o.protocol == nativeStreamOpenAIChat && len(o.finishedChoices) > 0 && len(o.openChoices) == 0 {
		o.state.ended = true
	}
	return o.state.err()
}

func (o *streamTerminalObserver) observeEvent(event []byte) {
	eventType, payload := sse.ParseEvent(event)
	o.state.noteFrame(event, eventType, payload)
	switch o.protocol {
	case nativeStreamAnthropic:
		kind := string(eventType)
		if kind == "" {
			kind = gjson.GetBytes(payload, "type").String()
		}
		switch kind {
		case anthropicMessageStartEvent:
			o.state.started = true
		case anthropicMessageStopEvent, streamCutErrorEvent:
			o.state.ended = true
		}
	case nativeStreamOpenAIChat:
		if len(payload) == 0 {
			return
		}
		if string(bytes.TrimSpace(payload)) == openAIChatDoneSentinel {
			o.state.ended = true
			return
		}
		o.state.started = true
		if upstreamErr := gjson.GetBytes(payload, "error"); upstreamErr.Exists() && upstreamErr.Type != gjson.Null {
			o.state.ended = true
			return
		}
		o.observeChatChoices(payload)
	}
}

func (o *streamTerminalObserver) observeChatChoices(payload []byte) {
	gjson.GetBytes(payload, "choices").ForEach(func(_, choice gjson.Result) bool {
		index := choice.Get("index").Int()
		if choice.Get("finish_reason").String() != "" {
			if o.finishedChoices == nil {
				o.finishedChoices = make(map[int64]struct{})
			}
			o.finishedChoices[index] = struct{}{}
			delete(o.openChoices, index)
			return true
		}
		if _, finished := o.finishedChoices[index]; !finished {
			if o.openChoices == nil {
				o.openChoices = make(map[int64]struct{})
			}
			o.openChoices[index] = struct{}{}
		}
		return true
	})
}

func isStreamTerminalMissing(err error) bool {
	return errors.Is(err, translate.ErrStreamIncomplete) || errors.Is(err, translate.ErrStreamEmpty)
}

var (
	_ http.ResponseWriter              = (*streamTerminalObserver)(nil)
	_ http.Flusher                     = (*streamTerminalObserver)(nil)
	_ providers.OutputProgressArmer    = (*streamTerminalObserver)(nil)
	_ providers.ReasoningProgressArmer = (*streamTerminalObserver)(nil)
)
