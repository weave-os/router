package proxy

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"
)

const (
	responsesCompletedEvent  = "response.completed"
	responsesIncompleteEvent = "response.incomplete"
	responsesFailedEvent     = "response.failed"
)

// responsesTerminalObserver tees a native /v1/responses stream to inner while
// reading the terminal event's turn-ending signals. The native passthrough runs
// no translator, so without this a turn reports no finish_reason at all, leaving
// a tool call, a completed answer, and a truncation indistinguishable.
type responsesTerminalObserver struct {
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
	state streamTerminalState
}

func newResponsesTerminalObserver(inner http.ResponseWriter) *responsesTerminalObserver {
	return &responsesTerminalObserver{inner: inner, state: streamTerminalState{observation: translate.NewProviderStreamObservation(translate.FormatOpenAIResponses)}}
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
func (o *responsesTerminalObserver) Finalize() {
	if o.buf.Len() == 0 {
		return
	}
	rest := o.buf.Bytes()
	o.buf.Reset()
	o.framing.Reset()
	o.observeEvent(rest)
}

// observeEvent records the same first terminal that owns lifecycle completion.
func (o *responsesTerminalObserver) observeEvent(event []byte) {
	switch o.state.observation.Facts().State {
	case translate.StreamTerminal, translate.StreamFailed, translate.StreamCanceled:
		return
	}
	_, payload := sse.ParseEvent(event)
	o.state.observation.ObserveEvent(event)
	if len(payload) == 0 {
		// A non-streaming body carries no SSE framing.
		payload = event
		o.state.observation.ObserveBody(event)
	}
	if !completeTailPayload(payload) {
		return
	}
	if signals, ok := translate.ResponsesTerminal(payload); ok {
		o.signals = signals
		o.observed = true
	}
}

// Cancel records the attempt context after consuming any complete final record.
func (o *responsesTerminalObserver) Cancel(cause error) {
	if cause == nil {
		return
	}
	o.Finalize()
	o.state.observation.Cancel(cause)
}

// DeclaredError reports provider rejection without imposing streaming EOF rules.
func (o *responsesTerminalObserver) DeclaredError() error {
	o.Finalize()
	return o.state.observation.DeclaredError()
}

func (o *responsesTerminalObserver) OutcomeError() error {
	o.Finalize()
	return o.state.observation.OutcomeError()
}

func (o *responsesTerminalObserver) streamErr() error {
	o.Finalize()
	return o.state.err()
}

var (
	_ http.ResponseWriter = (*responsesTerminalObserver)(nil)
	_ http.Flusher        = (*responsesTerminalObserver)(nil)
)
