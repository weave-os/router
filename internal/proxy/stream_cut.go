package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
)

// streamFailureClass names the owner of a stream that failed after the
// prelude committed. Derived from the error chain (typed sentinels, never
// message text) plus the last frame seen on the wire.
type streamFailureClass string

const (
	streamFailureUpstreamEOF            streamFailureClass = "upstream_eof"
	streamFailureUpstreamReset          streamFailureClass = "upstream_reset"
	streamFailureIdleWatchdog           streamFailureClass = "idle_watchdog"
	streamFailureOutputStallWatchdog    streamFailureClass = "output_stall_watchdog"
	streamFailureSlowThroughputWatchdog streamFailureClass = "slow_throughput_watchdog"
	streamFailureUpstreamErrorFrame     streamFailureClass = "upstream_error_frame"
	streamFailureClientCanceled         streamFailureClass = "client_canceled"
	streamFailureDeadline               streamFailureClass = "deadline"
	streamFailureOther                  streamFailureClass = "other"
)

// streamCutErrorEvent is the SSE event type an upstream uses to report an
// error inside an otherwise-200 stream.
const streamCutErrorEvent = "error"

// streamCutCarryCap bounds the partial-frame tail kept between writes. A
// frame larger than this is pathological, so the scanner resyncs on the next
// boundary instead of growing without bound.
const streamCutCarryCap = 64 * 1024

// streamCutObserver tees the writer a dispatch attempt hands to the provider,
// unchanged, while recording what the wire looked like when a committed
// stream died: frame count, last event type, and the gap since the last
// frame. It sits above the marker writer and the translators, so the frames
// it counts are the upstream's own and not the router's.
type streamCutObserver struct {
	inner http.ResponseWriter
	now   func() time.Time

	start        time.Time
	lastFrame    time.Time
	carry        []byte
	frames       int
	lastEvent    string
	streaming    bool
	cut          bool
	cutSnapshot  streamCutSnapshot
	cutClassSeen bool
}

type streamCutSnapshot struct {
	elapsed       time.Duration
	sinceLastData time.Duration
	frames        int
	lastEvent     string
	class         streamFailureClass
}

func newStreamCutObserver(now func() time.Time) *streamCutObserver {
	if now == nil {
		now = time.Now
	}
	return &streamCutObserver{now: now}
}

// attach arms the observer for one dispatch attempt on top of the attempt's
// upstream writer. Each attempt restarts counting, so a failed pre-commit
// attempt's frames never inflate the winner's.
func (o *streamCutObserver) attach(inner http.ResponseWriter) http.ResponseWriter {
	if o == nil {
		return inner
	}
	now := o.now()
	o.inner = inner
	o.start, o.lastFrame = now, now
	o.carry, o.frames, o.lastEvent, o.streaming = o.carry[:0], 0, "", true
	return o
}

func (o *streamCutObserver) Header() http.Header { return o.inner.Header() }

func (o *streamCutObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *streamCutObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress keeps the adapters' output-progress watchdog and
// first-output stamp reachable through the observer: a wrapper that hides the
// translator's armer silently downgrades the stream to byte-idle guarding.
func (o *streamCutObserver) ArmOutputProgress(mark func()) bool {
	arm, ok := o.inner.(providers.OutputProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

func (o *streamCutObserver) Write(p []byte) (int, error) {
	if o.streaming && len(p) > 0 {
		o.lastFrame = o.now()
		o.scan(p)
	}
	return o.inner.Write(p)
}

// noteCut freezes the wire state at the moment err surfaced, before a
// post-commit path replaces err with a synthesized status. First observation
// wins for the same reason.
func (o *streamCutObserver) noteCut(err error) {
	if o == nil || err == nil || o.cut {
		return
	}
	o.cut = true
	o.streaming = false
	o.cutSnapshot = streamCutSnapshot{
		elapsed:       o.now().Sub(o.start),
		sinceLastData: o.now().Sub(o.lastFrame),
		frames:        o.frames,
		lastEvent:     o.lastEvent,
		class:         classifyStreamFailure(err, o.lastEvent),
	}
	o.cutClassSeen = true
}

// completionLogFields returns the stream-cut diagnostics for the completion
// log line, or nil when the turn's stream never died after committing.
func (o *streamCutObserver) completionLogFields() []any {
	if !o.cutClassSeen {
		return nil
	}
	s := o.cutSnapshot
	return []any{
		"stream_cut_elapsed_ms", s.elapsed.Milliseconds(),
		"stream_ms_since_last_upstream_frame", s.sinceLastData.Milliseconds(),
		"stream_last_upstream_event", s.lastEvent,
		"stream_upstream_frames", s.frames,
		"stream_failure_class", string(s.class),
	}
}

func (o *streamCutObserver) scan(p []byte) {
	buf := p
	if len(o.carry) > 0 {
		o.carry = append(o.carry, p...)
		buf = o.carry
	}
	for {
		event, n := sse.SplitNext(buf)
		if n == 0 {
			break
		}
		o.frames++
		if eventType, _ := sse.ParseEvent(event); len(eventType) > 0 {
			o.lastEvent = string(eventType)
		}
		buf = buf[n:]
	}
	if len(buf) > streamCutCarryCap {
		buf = nil
	}
	o.carry = append(o.carry[:0], buf...)
}

// classifyStreamFailure names the owner of err. Watchdog sentinels come first:
// the watchdog aborts by cancelling the request context, so the chain also
// carries context.Canceled (same ordering as providers.IsRetryable).
func classifyStreamFailure(err error, lastEvent string) streamFailureClass {
	switch {
	case err == nil:
		return streamFailureOther
	case errors.Is(err, providers.ErrUpstreamIdleTimeout):
		return streamFailureIdleWatchdog
	case errors.Is(err, providers.ErrUpstreamOutputStall):
		return streamFailureOutputStallWatchdog
	case errors.Is(err, providers.ErrUpstreamSlowThroughput):
		return streamFailureSlowThroughputWatchdog
	case errors.Is(err, context.Canceled):
		return streamFailureClientCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return streamFailureDeadline
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, net.ErrClosed):
		return streamFailureUpstreamReset
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return streamFailureUpstreamEOF
	case isUpstreamStatusFailure(err), lastEvent == streamCutErrorEvent:
		return streamFailureUpstreamErrorFrame
	default:
		return streamFailureOther
	}
}

// isUpstreamStatusFailure reports whether the upstream named the failure
// itself (a status or a buffered error body) rather than dropping the
// transport under us.
func isUpstreamStatusFailure(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	var flushed *providers.UpstreamStatusError
	return errors.As(err, &buffered) || errors.As(err, &flushed)
}
