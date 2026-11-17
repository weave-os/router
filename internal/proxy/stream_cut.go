package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

type streamFailureClass string

const (
	streamFailureUpstreamEOF            streamFailureClass = "upstream_eof"
	streamFailureUpstreamReset          streamFailureClass = "upstream_reset"
	streamFailureIdleWatchdog           streamFailureClass = "idle_watchdog"
	streamFailureOutputStallWatchdog    streamFailureClass = "output_stall_watchdog"
	streamFailureSlowThroughputWatchdog streamFailureClass = "slow_throughput_watchdog"
	streamFailureUpstreamErrorFrame     streamFailureClass = "upstream_error_frame"
	streamFailureUpstreamIncomplete     streamFailureClass = "upstream_incomplete"
	streamFailureUpstreamEmpty          streamFailureClass = "upstream_empty"
	streamFailureUpstreamTimeout        streamFailureClass = "upstream_timeout"
	streamFailureClientCanceled         streamFailureClass = "client_canceled"
	streamFailureDeadline               streamFailureClass = "deadline"
	streamFailureOther                  streamFailureClass = "other"
)

const streamCutCarryCap = 64 * 1024

type streamCutObserver struct {
	inner http.ResponseWriter
	now   func() time.Time

	requestBytes int
	thinking     bool

	start              time.Time
	lastFrame          time.Time
	carry              []byte
	framing            sse.Scanner
	frames             int
	lastEvent          string
	blocksCompleted    int
	outputBlockStarted bool
	streaming          bool
	cut                bool
	cutSnapshot        streamCutSnapshot
	cutClassSeen       bool
}

type streamCutSnapshot struct {
	elapsed            time.Duration
	sinceLastData      time.Duration
	frames             int
	lastEvent          string
	blocksCompleted    int
	outputBlockStarted bool
	class              streamFailureClass
	retryable          bool
}

func newStreamCutObserver(now func() time.Time) *streamCutObserver {
	if now == nil {
		now = time.Now
	}
	return &streamCutObserver{now: now}
}

func (o *streamCutObserver) describeRequest(requestBytes int, env *translate.RequestEnvelope) {
	if o == nil {
		return
	}
	o.requestBytes = requestBytes
	o.thinking = reasoningRequested(env)
}

func (o *streamCutObserver) attach(inner http.ResponseWriter) http.ResponseWriter {
	if o == nil {
		return inner
	}
	now := o.now()
	o.inner = inner
	o.start, o.lastFrame = now, now
	o.carry, o.frames, o.lastEvent, o.streaming = o.carry[:0], 0, "", true
	o.framing.Reset()
	o.blocksCompleted, o.outputBlockStarted = 0, false
	return o
}

func (o *streamCutObserver) Header() http.Header { return o.inner.Header() }

func (o *streamCutObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *streamCutObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *streamCutObserver) ArmOutputProgress(mark func()) bool {
	arm, ok := o.inner.(providers.OutputProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

func (o *streamCutObserver) ArmReasoningProgress(mark func()) bool {
	arm, ok := o.inner.(providers.ReasoningProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmReasoningProgress(mark)
}

func (o *streamCutObserver) Write(p []byte) (int, error) {
	if o.streaming && len(p) > 0 {
		o.lastFrame = o.now()
		o.scan(p)
	}
	return o.inner.Write(p)
}

func (o *streamCutObserver) noteCut(err error) {
	if o == nil || err == nil || o.cut {
		return
	}
	o.cut = true
	o.streaming = false
	o.cutSnapshot = streamCutSnapshot{
		elapsed:            o.now().Sub(o.start),
		sinceLastData:      o.now().Sub(o.lastFrame),
		frames:             o.frames,
		lastEvent:          o.lastEvent,
		blocksCompleted:    o.blocksCompleted,
		outputBlockStarted: o.outputBlockStarted,
		class:              classifyStreamFailure(err, o.lastEvent),
	}
	o.cutSnapshot.retryable = streamCutReplayRetryable(o.cutSnapshot.class, err)
	o.cutClassSeen = true
}

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
		"stream_cut_request_bytes", o.requestBytes,
		"stream_cut_thinking", o.thinking,
		"stream_cut_replay_retryable", s.retryable,
		"stream_upstream_blocks_completed", s.blocksCompleted,
		"stream_upstream_output_block_started", s.outputBlockStarted,
	}
}

func reasoningRequested(env *translate.RequestEnvelope) bool {
	if env == nil {
		return false
	}
	kind := env.ReasoningIntent().Kind
	return kind != "" && kind != translate.ReasoningDisabled
}

func streamCutReplayRetryable(class streamFailureClass, err error) bool {
	switch class {
	case streamFailureClientCanceled, streamFailureDeadline:
		return false
	default:
	}
	status := upstreamStatus(err)
	return status == 0 || providers.IsRetryableStatus(status)
}

func (o *streamCutObserver) scan(p []byte) {
	buf := p
	if len(o.carry) > 0 {
		o.carry = append(o.carry, p...)
		buf = o.carry
	}
	for {
		event, n := o.framing.Next(buf)
		if n == 0 {
			break
		}
		o.frames++
		eventType, data := sse.ParseEvent(event)
		if len(eventType) > 0 {
			o.lastEvent = string(eventType)
		}
		switch string(eventType) {
		case "content_block_stop":
			o.blocksCompleted++
		case "content_block_start":
			switch gjson.GetBytes(data, "content_block.type").String() {
			case "thinking", "redacted_thinking":
			default:
				o.outputBlockStarted = true
			}
		}
		buf = buf[n:]
	}
	if len(buf) > streamCutCarryCap {
		buf = nil
		o.framing.Reset()
	}
	o.carry = append(o.carry[:0], buf...)
}

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
	case errors.Is(err, translate.ErrStreamIncomplete):
		return streamFailureUpstreamIncomplete
	case errors.Is(err, translate.ErrStreamEmpty):
		return streamFailureUpstreamEmpty
	case errors.Is(err, context.Canceled):
		return streamFailureClientCanceled
	case providers.IsResponseHeaderTimeout(err):
		return streamFailureUpstreamTimeout
	case errors.Is(err, context.DeadlineExceeded):
		return streamFailureDeadline
	case isNetTimeout(err):
		return streamFailureUpstreamTimeout
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, net.ErrClosed),
		providers.IsHTTP2ConnectionLoss(err):
		return streamFailureUpstreamReset
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return streamFailureUpstreamEOF
	case isUpstreamStatusFailure(err), lastEvent == ingress.StreamErrorEvent:
		return streamFailureUpstreamErrorFrame
	default:
		return streamFailureOther
	}
}

func isNetTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isUpstreamStatusFailure(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	var flushed *providers.UpstreamStatusError
	return errors.As(err, &buffered) || errors.As(err, &flushed)
}
