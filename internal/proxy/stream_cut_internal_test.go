package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"syscall"
	"testing"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/telemetry/turnrecord"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"golang.org/x/net/http2"
)

func TestClassifyStreamFailure(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		lastEvent string
		want      dispatch.StreamFailureClass
	}{
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, lastEvent: "content_block_delta", want: dispatch.StreamFailureUpstreamEOF},
		{name: "eof wrapped by the adapter", err: fmt.Errorf("read upstream: %w", io.EOF), lastEvent: "ping", want: dispatch.StreamFailureUpstreamEOF},
		{name: "peer reset", err: fmt.Errorf("read tcp: %w", syscall.ECONNRESET), want: dispatch.StreamFailureUpstreamReset},
		{name: "idle watchdog", err: providers.ErrUpstreamIdleTimeout, want: dispatch.StreamFailureIdleWatchdog},
		{
			// The watchdog aborts by cancelling the request context, so the
			// chain carries context.Canceled too; the sentinel must still win.
			name: "output stall watchdog under a cancelled context",
			err:  fmt.Errorf("%w: %w", providers.ErrUpstreamOutputStall, context.Canceled),
			want: dispatch.StreamFailureOutputStallWatchdog,
		},
		{name: "slow throughput watchdog", err: providers.ErrUpstreamSlowThroughput, want: dispatch.StreamFailureSlowThroughputWatchdog},
		{name: "client disconnect", err: context.Canceled, lastEvent: "content_block_delta", want: dispatch.StreamFailureClientCanceled},
		{name: "request deadline", err: context.DeadlineExceeded, want: dispatch.StreamFailureDeadline},
		{name: "stream without a terminal event", err: translate.ErrStreamIncomplete, lastEvent: "content_block_delta", want: dispatch.StreamFailureUpstreamIncomplete},
		{name: "stream that never started", err: translate.ErrStreamEmpty, want: dispatch.StreamFailureUpstreamEmpty},
		{name: "http/1 response-header timeout", err: http1HeaderTimeout(), want: dispatch.StreamFailureUpstreamTimeout},
		{name: "http/2 response-header timeout", err: http2HeaderTimeout(), want: dispatch.StreamFailureUpstreamTimeout},
		{name: "transport read timeout", err: &url.Error{Op: "Post", URL: "https://upstream.test", Err: headerTimeoutError{msg: "read tcp 10.0.0.1:443: i/o timeout"}}, want: dispatch.StreamFailureUpstreamTimeout},
		{name: "upstream status after commit", err: &providers.UpstreamStatusError{Status: 502}, want: dispatch.StreamFailureUpstreamErrorFrame},
		{name: "upstream error event then transport failure", err: errors.New("stream closed"), lastEvent: ingress.StreamErrorEvent, want: dispatch.StreamFailureUpstreamErrorFrame},
		{name: "http/2 keepalive ping lost", err: http2LostPing(), want: dispatch.StreamFailureUpstreamReset},
		{name: "http/2 keepalive ping lost mid-stream", err: fmt.Errorf("read upstream stream: %w", errors.New("http2: client connection lost")), lastEvent: "content_block_delta", want: dispatch.StreamFailureUpstreamReset},
		{name: "http/2 stream reset by the peer", err: http2PeerStreamReset(), want: dispatch.StreamFailureUpstreamReset},
		{name: "http/2 goaway", err: http2GoAway(), want: dispatch.StreamFailureUpstreamReset},
		{name: "unattributable", err: errors.New("stream closed"), lastEvent: "content_block_delta", want: dispatch.StreamFailureOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, dispatch.ClassifyStreamFailure(tc.err, tc.lastEvent))
		})
	}
}

func TestClassifyTurnError(t *testing.T) {
	cases := []struct {
		name            string
		err             error
		stopReason      string
		invalidToolArgs int
		want            turnrecord.TurnErrorClass
	}{
		{name: "normal completion", stopReason: "end_turn", want: ""},
		{name: "rate limited", err: &providers.UpstreamStatusError{Status: 429}, want: turnrecord.TurnErrorRateLimited},
		{name: "invalid request", err: &providers.UpstreamStatusError{Status: 400}, want: turnrecord.TurnErrorInvalidRequest},
		{name: "unknown model", err: &providers.UpstreamStatusError{Status: 404}, want: turnrecord.TurnErrorModelNotFound},
		{name: "rejected credential", err: &providers.UpstreamStatusError{Status: 401}, want: turnrecord.TurnErrorAuthRejected},
		{name: "overloaded", err: &providers.UpstreamStatusError{Status: 529}, want: turnrecord.TurnErrorUpstream5xx},
		{name: "other 4xx", err: &providers.UpstreamStatusError{Status: 409}, want: turnrecord.TurnErrorUpstream4xx},
		{name: "status wins over the stream owner", err: fmt.Errorf("%w: %w", &providers.UpstreamStatusError{Status: 502}, context.Canceled), want: turnrecord.TurnErrorUpstream5xx},
		{name: "cut stream", err: io.ErrUnexpectedEOF, want: turnrecord.TurnErrorStreamCut},
		{name: "watchdog abort is not a client cancel", err: fmt.Errorf("%w: %w", providers.ErrUpstreamIdleTimeout, context.Canceled), want: turnrecord.TurnErrorStreamStalled},
		{name: "client cancel", err: context.Canceled, want: turnrecord.TurnErrorClientCanceled},
		{name: "deadline", err: context.DeadlineExceeded, want: turnrecord.TurnErrorTimeout},
		{name: "client cancel during the upstream request", err: &url.Error{Op: "Post", URL: "https://upstream.test", Err: context.Canceled}, want: turnrecord.TurnErrorClientCanceled},
		{name: "http/1 response-header timeout", err: http1HeaderTimeout(), want: turnrecord.TurnErrorTimeout},
		{name: "http/2 response-header timeout", err: http2HeaderTimeout(), want: turnrecord.TurnErrorTimeout},
		{name: "stream without a terminal event", err: translate.ErrStreamIncomplete, want: turnrecord.TurnErrorStreamCut},
		{name: "stream that never started", err: translate.ErrStreamEmpty, want: turnrecord.TurnErrorStreamCut},
		{name: "http/2 keepalive ping lost", err: http2LostPing(), want: turnrecord.TurnErrorStreamCut},
		{name: "unattributable failure", err: errors.New("stream closed"), want: turnrecord.TurnErrorOther},
		{name: "refusal", stopReason: "refusal", want: turnrecord.TurnErrorRefusal},
		{name: "openai output cap", stopReason: "length", want: turnrecord.TurnErrorMaxTokens},
		{name: "malformed tool call", stopReason: "tool_use", invalidToolArgs: 1, want: turnrecord.TurnErrorInvalidToolArgs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyTurnError(tc.err, tc.stopReason, tc.invalidToolArgs))
		})
	}
}

// headerTimeoutError mirrors net/http's HTTP/2 response-header timeout: a
// net.Error reporting Timeout() with no context.DeadlineExceeded in its chain.
type headerTimeoutError struct{ msg string }

func (e headerTimeoutError) Error() string { return e.msg }

func (e headerTimeoutError) Timeout() bool { return true }

func (e headerTimeoutError) Temporary() bool { return true }

func http2HeaderTimeout() error {
	return &url.Error{Op: "Post", URL: "https://upstream.test", Err: headerTimeoutError{msg: "http2: timeout awaiting response headers"}}
}

// http2LostPing mirrors x/net/http2's error when the provider stops answering
// keepalive pings; it is an untyped errors.New.
func http2LostPing() error {
	return &url.Error{Op: "Post", URL: "https://upstream.test", Err: errors.New("http2: client connection lost")}
}

// http2GoAway mirrors net/http's bundled transport error after a GOAWAY frame.
func http2GoAway() error {
	return &url.Error{Op: "Post", URL: "https://upstream.test", Err: errors.New(`http2: server sent GOAWAY and closed the connection; LastStreamID=1, ErrCode=ENHANCE_YOUR_CALM, debug=""`)}
}

func http2PeerStreamReset() error {
	return fmt.Errorf("stream error: %w", http2.StreamError{StreamID: 3337, Code: http2.ErrCodeInternal})
}

// http1HeaderTimeout mirrors the HTTP/1 transport's error, which also matches
// context.DeadlineExceeded.
type http1TimeoutError struct{}

func (http1TimeoutError) Error() string { return "net/http: timeout awaiting response headers" }

func (http1TimeoutError) Timeout() bool { return true }

func (http1TimeoutError) Is(target error) bool { return target == context.DeadlineExceeded }

func http1HeaderTimeout() error {
	return &url.Error{Op: "Post", URL: "https://upstream.test", Err: http1TimeoutError{}}
}

func TestClassifyTurnErrorReadsCutThroughSyntheticFrame(t *testing.T) {
	for _, err := range []error{translate.ErrStreamIncomplete, translate.ErrStreamEmpty} {
		framed := emitAnthropicSSEErrorEvent(httptest.NewRecorder(), err)
		assert.Equal(t, turnrecord.TurnErrorStreamCut, classifyTurnError(framed, "", 0),
			"the router's 502 frame stands in for the cut, not an upstream 5xx")
	}
}

func TestClassifyTurnErrorDistinguishesWatchdogFrameFromUpstreamStatus(t *testing.T) {
	err := emitAnthropicSSEErrorEvent(httptest.NewRecorder(), providers.ErrUpstreamIdleTimeout)

	assert.Equal(t, turnrecord.TurnErrorStreamStalled, classifyTurnError(err, "", 0),
		"a router-generated 502 frame does not mean the upstream returned 502")
	assert.Equal(t, turnrecord.TurnErrorUpstream5xx, classifyTurnError(&providers.UpstreamStatusError{Status: http.StatusBadGateway}, "", 0))
}

type armerRecorder struct {
	*httptest.ResponseRecorder
	marks int
}

func (a *armerRecorder) ArmOutputProgress(mark func()) bool {
	a.marks++
	mark()
	return true
}
