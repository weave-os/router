package ingress

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

const (
	responsesCompletedEvent  = "response.completed"
	responsesIncompleteEvent = "response.incomplete"
	responsesFailedEvent     = "response.failed"
)

// ResponsesTerminalObserver tees a native /v1/responses stream to inner while
// reading the terminal event's turn-ending signals. The native passthrough runs
// no translator, so without this a turn reports no finish_reason at all, leaving
// a tool call, a completed answer, and a truncation indistinguishable.
type ResponsesTerminalObserver struct {
	inner http.ResponseWriter
	// buf holds the bytes of the event currently being assembled; SSE frames
	// arrive split across writes.
	buf bytes.Buffer
	// framing resumes the delimiter search where the previous write left it,
	// so a frame spread over many writes is scanned once, not once per write.
	framing sse.Scanner
	// signals are the chat-shaped reason and tool-call count from the terminal
	// event; observed stays false until one arrives (and on a failed terminal,
	// whose outcome is the error), so a cut stream is never read as a measured
	// zero-tool turn.
	signals  translate.ResponsesTerminalSignals
	observed bool
	// state also counts a failed terminal and an error event: either names the
	// outcome on the wire, unlike a stream the upstream simply stopped.
	state StreamTerminalState
}

func NewResponsesTerminalObserver(inner http.ResponseWriter) *ResponsesTerminalObserver {
	return &ResponsesTerminalObserver{inner: inner}
}

func (o *ResponsesTerminalObserver) Header() http.Header { return o.inner.Header() }

func (o *ResponsesTerminalObserver) WriteHeader(status int) {
	sse.PrepareJSONResponse(o.inner)
	o.inner.WriteHeader(status)
}

func (o *ResponsesTerminalObserver) Write(p []byte) (int, error) {
	o.state.NoteWrite(p)
	o.scan(p)
	return sse.WriteJSONChunk(o.inner, p)
}

func (o *ResponsesTerminalObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress forwards the watchdog hook: this writer cannot classify an
// output frame, so hiding the hook from the provider client would disarm the
// output-stall watchdog on every native Responses turn.
func (o *ResponsesTerminalObserver) ArmOutputProgress(mark func()) (armed bool) {
	arm, ok := o.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// scan consumes whole SSE events, keeping the trailing partial frame buffered.
// A non-streaming body never completes a frame, so it is read at Finalize.
func (o *ResponsesTerminalObserver) scan(p []byte) {
	o.buf.Write(p)
	for {
		event, n := o.framing.Next(o.buf.Bytes())
		if n == 0 {
			return
		}
		o.observeEvent(event)
		o.buf.Next(n)
	}
}

// Finalize reads a terminal event that arrived without a trailing blank line,
// covering the non-streaming JSON body and a stream whose last frame is
// unterminated. Call once the upstream call has returned.
func (o *ResponsesTerminalObserver) Finalize() {
	if o.buf.Len() == 0 {
		return
	}
	rest := o.buf.Bytes()
	o.buf.Reset()
	o.framing.Reset()
	o.observeEvent(rest)
}

// observeEvent records the signals from a terminal event. A later terminal
// event wins: an upstream that revises the envelope states its outcome last.
func (o *ResponsesTerminalObserver) observeEvent(event []byte) {
	eventType, payload := sse.ParseEvent(event)
	o.state.NoteFrame(event, eventType, payload)
	if len(payload) == 0 {
		// A non-streaming body carries no SSE framing.
		payload = event
	}
	if !CompleteTailPayload(payload) {
		return
	}
	kind := gjson.GetBytes(payload, "type").String()
	if kind == "" {
		kind = string(eventType)
	}
	switch kind {
	case "":
	case responsesCompletedEvent, responsesIncompleteEvent, responsesFailedEvent, StreamErrorEvent:
		o.state.MarkStarted()
		o.state.MarkEnded()
	default:
		o.state.MarkStarted()
	}
	if signals, ok := translate.ResponsesTerminal(payload); ok {
		o.signals = signals
		o.observed = true
	}
}

// Observed reports whether a terminal event carrying turn signals arrived.
func (o *ResponsesTerminalObserver) Observed() bool { return o.observed }

// Signals are the terminal event's chat-shaped finish reason and tool calls.
func (o *ResponsesTerminalObserver) Signals() translate.ResponsesTerminalSignals { return o.signals }

// Err reports how the stream ended; see StreamTerminalState.Err.
func (o *ResponsesTerminalObserver) Err() error { return o.state.Err() }

var (
	_ http.ResponseWriter = (*ResponsesTerminalObserver)(nil)
	_ http.Flusher        = (*ResponsesTerminalObserver)(nil)
)
