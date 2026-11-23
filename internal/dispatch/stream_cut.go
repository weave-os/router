package dispatch

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

type StreamFailureClass string

const (
	StreamFailureUpstreamEOF            StreamFailureClass = "upstream_eof"
	StreamFailureUpstreamReset          StreamFailureClass = "upstream_reset"
	StreamFailureIdleWatchdog           StreamFailureClass = "idle_watchdog"
	StreamFailureOutputStallWatchdog    StreamFailureClass = "output_stall_watchdog"
	StreamFailureSlowThroughputWatchdog StreamFailureClass = "slow_throughput_watchdog"
	StreamFailureUpstreamErrorFrame     StreamFailureClass = "upstream_error_frame"
	StreamFailureUpstreamIncomplete     StreamFailureClass = "upstream_incomplete"
	StreamFailureUpstreamEmpty          StreamFailureClass = "upstream_empty"
	StreamFailureUpstreamTimeout        StreamFailureClass = "upstream_timeout"
	StreamFailureClientCanceled         StreamFailureClass = "client_canceled"
	StreamFailureDeadline               StreamFailureClass = "deadline"
	StreamFailureOther                  StreamFailureClass = "other"
)

const streamCutCarryCap = 64 * 1024

type StreamCutObserver struct {
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
	class              StreamFailureClass
	retryable          bool
}

func NewStreamCutObserver(now func() time.Time) *StreamCutObserver {
	if now == nil {
		now = time.Now
	}
	return &StreamCutObserver{now: now}
}

func (o *StreamCutObserver) DescribeRequest(requestBytes int, env *translate.RequestEnvelope) {
	if o == nil {
		return
	}
	o.requestBytes = requestBytes
	o.thinking = reasoningRequested(env)
}

func (o *StreamCutObserver) Attach(inner http.ResponseWriter) http.ResponseWriter {
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

func (o *StreamCutObserver) Header() http.Header { return o.inner.Header() }

func (o *StreamCutObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *StreamCutObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *StreamCutObserver) ArmOutputProgress(mark func()) bool {
	arm, ok := o.inner.(providers.OutputProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

func (o *StreamCutObserver) ArmReasoningProgress(mark func()) bool {
	arm, ok := o.inner.(providers.ReasoningProgressArmer)
	if !ok {
		return false
	}
	return arm.ArmReasoningProgress(mark)
}

func (o *StreamCutObserver) Write(p []byte) (int, error) {
	if o.streaming && len(p) > 0 {
		o.lastFrame = o.now()
		o.scan(p)
	}
	return o.inner.Write(p)
}

func (o *StreamCutObserver) NoteCut(err error) {
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
		class:              ClassifyStreamFailure(err, o.lastEvent),
	}
	o.cutSnapshot.retryable = streamCutReplayRetryable(o.cutSnapshot.class, err)
	o.cutClassSeen = true
}

func (o *StreamCutObserver) CompletionLogFields() []any {
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

func streamCutReplayRetryable(class StreamFailureClass, err error) bool {
	switch class {
	case StreamFailureClientCanceled, StreamFailureDeadline:
		return false
	default:
	}
	status := providers.UpstreamStatus(err)
	return status == 0 || providers.IsRetryableStatus(status)
}

func (o *StreamCutObserver) scan(p []byte) {
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

func ClassifyStreamFailure(err error, lastEvent string) StreamFailureClass {
	switch {
	case err == nil:
		return StreamFailureOther
	case errors.Is(err, providers.ErrUpstreamIdleTimeout):
		return StreamFailureIdleWatchdog
	case errors.Is(err, providers.ErrUpstreamOutputStall):
		return StreamFailureOutputStallWatchdog
	case errors.Is(err, providers.ErrUpstreamSlowThroughput):
		return StreamFailureSlowThroughputWatchdog
	case errors.Is(err, translate.ErrStreamIncomplete):
		return StreamFailureUpstreamIncomplete
	case errors.Is(err, translate.ErrStreamEmpty):
		return StreamFailureUpstreamEmpty
	case errors.Is(err, context.Canceled):
		return StreamFailureClientCanceled
	case providers.IsResponseHeaderTimeout(err):
		return StreamFailureUpstreamTimeout
	case errors.Is(err, context.DeadlineExceeded):
		return StreamFailureDeadline
	case isNetTimeout(err):
		return StreamFailureUpstreamTimeout
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, net.ErrClosed),
		providers.IsHTTP2ConnectionLoss(err):
		return StreamFailureUpstreamReset
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return StreamFailureUpstreamEOF
	case isUpstreamStatusFailure(err), lastEvent == ingress.StreamErrorEvent:
		return StreamFailureUpstreamErrorFrame
	default:
		return StreamFailureOther
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
