package proxy

import (
	"bytes"
	"errors"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

// streamTerminalState keeps the completion verdict separate from provider failure.
type streamTerminalState struct {
	observation *translate.ProviderStreamObservation
}

func (s streamTerminalState) err() error { return s.observation.EOF() }

// completeTailPayload excludes a truncated terminal from completion evidence.
func completeTailPayload(payload []byte) bool {
	return string(bytes.TrimSpace(payload)) == "[DONE]" || gjson.ValidBytes(payload)
}

// streamTerminalObserver tees native provider records unchanged while sharing
// lifecycle observation with response translators.
type streamTerminalObserver struct {
	inner http.ResponseWriter
	// buf holds the bytes of the frame currently being assembled; SSE frames
	// arrive split across writes.
	buf     bytes.Buffer
	framing sse.Scanner
	state   streamTerminalState
}

func newStreamTerminalObserver(inner http.ResponseWriter, protocol translate.Format) *streamTerminalObserver {
	return &streamTerminalObserver{inner: inner, state: streamTerminalState{observation: translate.NewProviderStreamObservation(protocol)}}
}

func (o *streamTerminalObserver) Header() http.Header { return o.inner.Header() }

func (o *streamTerminalObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *streamTerminalObserver) Write(p []byte) (int, error) {
	o.buf.Write(p)
	// JSON responses require whole-body validation; the carry cap below only
	// bounds partial SSE records, not complete buffered responses.
	if strings.Contains(o.Header().Get("Content-Type"), "application/json") {
		return o.inner.Write(p)
	}
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

// streamErr validates the final record once the provider call has returned.
func (o *streamTerminalObserver) streamErr() error {
	if o.buf.Len() > 0 {
		rest := bytes.Clone(o.buf.Bytes())
		o.buf.Reset()
		o.framing.Reset()
		o.state.observation.ObserveEvent(rest)
		o.state.observation.ObserveBody(rest)
	}
	return o.state.err()
}

// Cancel records the attempt context after consuming any complete final record.
func (o *streamTerminalObserver) Cancel(cause error) {
	if cause == nil {
		return
	}
	_ = o.streamErr()
	o.state.observation.Cancel(cause)
}

// DeclaredError reports provider rejection without imposing streaming EOF rules.
func (o *streamTerminalObserver) DeclaredError() error {
	_ = o.streamErr()
	return o.state.observation.DeclaredError()
}

func (o *streamTerminalObserver) OutcomeError() error {
	_ = o.streamErr()
	return o.state.observation.OutcomeError()
}

func (o *streamTerminalObserver) observeEvent(event []byte) {
	o.state.observation.ObserveEvent(event)
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
