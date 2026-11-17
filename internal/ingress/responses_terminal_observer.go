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

type ResponsesTerminalObserver struct {
	inner    http.ResponseWriter
	buf      bytes.Buffer
	framing  sse.Scanner
	signals  translate.ResponsesTerminalSignals
	observed bool
	state    StreamTerminalState
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

func (o *ResponsesTerminalObserver) ArmOutputProgress(mark func()) (armed bool) {
	arm, ok := o.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

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

func (o *ResponsesTerminalObserver) Finalize() {
	if o.buf.Len() == 0 {
		return
	}
	rest := o.buf.Bytes()
	o.buf.Reset()
	o.framing.Reset()
	o.observeEvent(rest)
}

func (o *ResponsesTerminalObserver) observeEvent(event []byte) {
	eventType, payload := sse.ParseEvent(event)
	o.state.NoteFrame(event, eventType, payload)
	if len(payload) == 0 {
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

func (o *ResponsesTerminalObserver) Observed() bool { return o.observed }

func (o *ResponsesTerminalObserver) Signals() translate.ResponsesTerminalSignals { return o.signals }

func (o *ResponsesTerminalObserver) Err() error { return o.state.Err() }

var (
	_ http.ResponseWriter = (*ResponsesTerminalObserver)(nil)
	_ http.Flusher        = (*ResponsesTerminalObserver)(nil)
)
