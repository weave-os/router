package proxy

import (
	"bytes"
	"net/http"

	"workweave/router/internal/sse"
	"workweave/router/internal/translate"
)

// responsesTerminalObserver tees a native /v1/responses stream to inner while
// reading the terminal event's finish reason. The native passthrough runs no
// translator, so without this a turn reports no finish_reason at all, leaving a
// tool call, a completed answer, and a truncation indistinguishable.
type responsesTerminalObserver struct {
	inner http.ResponseWriter
	// buf holds the bytes of the event currently being assembled; SSE frames
	// arrive split across writes.
	buf bytes.Buffer
	// finishReason is the chat-shaped reason from the terminal event, empty
	// until one arrives (and on a failed terminal, whose outcome is the error).
	finishReason string
}

func newResponsesTerminalObserver(inner http.ResponseWriter) *responsesTerminalObserver {
	return &responsesTerminalObserver{inner: inner}
}

func (o *responsesTerminalObserver) Header() http.Header { return o.inner.Header() }

func (o *responsesTerminalObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *responsesTerminalObserver) Write(p []byte) (int, error) {
	o.scan(p)
	return o.inner.Write(p)
}

func (o *responsesTerminalObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress forwards the watchdog hook: this writer cannot classify an
// output frame, so hiding the hook from the provider client would disarm the
// output-stall watchdog on every native Responses turn.
func (o *responsesTerminalObserver) ArmOutputProgress(mark func()) (armed bool) {
	arm, ok := o.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// scan consumes whole SSE events, keeping the trailing partial frame buffered.
// A non-streaming body never completes a frame, so it is read at Finalize.
func (o *responsesTerminalObserver) scan(p []byte) {
	o.buf.Write(p)
	for {
		event, n := sse.SplitNext(o.buf.Bytes())
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
func (o *responsesTerminalObserver) Finalize() {
	if o.buf.Len() == 0 {
		return
	}
	rest := o.buf.Bytes()
	o.buf.Reset()
	o.observeEvent(rest)
}

// observeEvent records the reason from a terminal event. A later terminal event
// wins: an upstream that revises the envelope states its outcome last.
func (o *responsesTerminalObserver) observeEvent(event []byte) {
	_, payload := sse.ParseEvent(event)
	if len(payload) == 0 {
		// A non-streaming body carries no SSE framing.
		payload = event
	}
	if reason, ok := translate.ResponsesTerminalReason(payload); ok {
		o.finishReason = reason
	}
}

var (
	_ http.ResponseWriter = (*responsesTerminalObserver)(nil)
	_ http.Flusher        = (*responsesTerminalObserver)(nil)
)
