package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyStreamFailure(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		lastEvent string
		want      streamFailureClass
	}{
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, lastEvent: "content_block_delta", want: streamFailureUpstreamEOF},
		{name: "eof wrapped by the adapter", err: fmt.Errorf("read upstream: %w", io.EOF), lastEvent: "ping", want: streamFailureUpstreamEOF},
		{name: "peer reset", err: fmt.Errorf("read tcp: %w", syscall.ECONNRESET), want: streamFailureUpstreamReset},
		{name: "idle watchdog", err: providers.ErrUpstreamIdleTimeout, want: streamFailureIdleWatchdog},
		{
			// The watchdog aborts by cancelling the request context, so the
			// chain carries context.Canceled too; the sentinel must still win.
			name: "output stall watchdog under a cancelled context",
			err:  fmt.Errorf("%w: %w", providers.ErrUpstreamOutputStall, context.Canceled),
			want: streamFailureOutputStallWatchdog,
		},
		{name: "slow throughput watchdog", err: providers.ErrUpstreamSlowThroughput, want: streamFailureSlowThroughputWatchdog},
		{name: "client disconnect", err: context.Canceled, lastEvent: "content_block_delta", want: streamFailureClientCanceled},
		{name: "request deadline", err: context.DeadlineExceeded, want: streamFailureDeadline},
		{name: "upstream status after commit", err: &providers.UpstreamStatusError{Status: 502}, want: streamFailureUpstreamErrorFrame},
		{name: "upstream error event then transport failure", err: errors.New("stream closed"), lastEvent: streamCutErrorEvent, want: streamFailureUpstreamErrorFrame},
		{name: "unattributable", err: errors.New("stream closed"), lastEvent: "content_block_delta", want: streamFailureOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyStreamFailure(tc.err, tc.lastEvent))
		})
	}
}

// Retryability describes a fresh replay of the same request, not the committed
// turn: caller-side cuts are never worth replaying, an upstream that named a
// non-retryable status has already answered, and everything else is the
// upstream's to fail again.
func TestStreamCutReplayRetryable(t *testing.T) {
	cases := []struct {
		name  string
		class streamFailureClass
		err   error
		want  bool
	}{
		{name: "upstream eof", class: streamFailureUpstreamEOF, err: io.ErrUnexpectedEOF, want: true},
		{name: "upstream reset", class: streamFailureUpstreamReset, err: syscall.ECONNRESET, want: true},
		{name: "idle watchdog", class: streamFailureIdleWatchdog, err: providers.ErrUpstreamIdleTimeout, want: true},
		{name: "output stall watchdog", class: streamFailureOutputStallWatchdog, err: providers.ErrUpstreamOutputStall, want: true},
		{name: "client disconnect", class: streamFailureClientCanceled, err: context.Canceled, want: false},
		{name: "request deadline", class: streamFailureDeadline, err: context.DeadlineExceeded, want: false},
		{name: "flushed 502", class: streamFailureUpstreamErrorFrame, err: &providers.UpstreamStatusError{Status: 502}, want: true},
		{name: "flushed 529 overload", class: streamFailureUpstreamErrorFrame, err: &providers.UpstreamStatusError{Status: 529}, want: true},
		{name: "flushed 400", class: streamFailureUpstreamErrorFrame, err: &providers.UpstreamStatusError{Status: 400}, want: false},
		{name: "error event without a status", class: streamFailureUpstreamErrorFrame, err: errors.New("stream closed"), want: true},
		{name: "unattributable transport error", class: streamFailureOther, err: errors.New("stream closed"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, streamCutReplayRetryable(tc.class, tc.err))
		})
	}
}

func TestReasoningRequested(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "no thinking field", body: `{"model":"m","messages":[]}`, want: false},
		{name: "thinking disabled", body: `{"thinking":{"type":"disabled"}}`, want: false},
		{name: "thinking budget", body: `{"thinking":{"type":"enabled","budget_tokens":4096}}`, want: true},
		{name: "adaptive thinking", body: `{"thinking":{"type":"adaptive"}}`, want: true},
		{name: "openai reasoning effort", body: `{"reasoning_effort":"high"}`, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, reasoningRequested(env))
		})
	}
	assert.False(t, reasoningRequested(nil))
}

// The observer must describe the wire as the upstream left it: a frame split
// across two writes counted once, the gap measured from the last frame, and
// anything written after the cut left out.
func TestStreamCutObserver_SnapshotsWireStateAtTheCut(t *testing.T) {
	clock := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	obs := newStreamCutObserver(func() time.Time { return clock })
	turn, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-8","thinking":{"type":"enabled","budget_tokens":2048},"messages":[]}`))
	require.NoError(t, err)
	obs.describeRequest(48213, turn)
	attempt := obs.attach(rec)

	clock = clock.Add(2 * time.Second)
	_, err = attempt.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: {\"in"))
	require.NoError(t, err)
	clock = clock.Add(58 * time.Second)
	_, err = attempt.Write([]byte("dex\":0}\n\nevent: ping\ndata: {}\n\n"))
	require.NoError(t, err)

	clock = clock.Add(4 * time.Second)
	obs.noteCut(io.ErrUnexpectedEOF)
	_, err = attempt.Write([]byte("event: error\ndata: {\"type\":\"error\"}\n\n"))
	require.NoError(t, err)

	assert.Equal(t, []any{
		"stream_cut_elapsed_ms", int64(64000),
		"stream_ms_since_last_upstream_frame", int64(4000),
		"stream_last_upstream_event", "ping",
		"stream_upstream_frames", 3,
		"stream_failure_class", string(streamFailureUpstreamEOF),
		"stream_cut_request_bytes", 48213,
		"stream_cut_thinking", true,
		"stream_cut_replay_retryable", true,
	}, obs.completionLogFields())
	assert.Equal(t,
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n"+
			"event: content_block_delta\ndata: {\"index\":0}\n\n"+
			"event: ping\ndata: {}\n\n"+
			"event: error\ndata: {\"type\":\"error\"}\n\n",
		rec.Body.String(), "the observer must pass every byte through unchanged")
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

// The provider adapters arm the output-stall watchdog and the first-output
// latency stamp through an optional interface on the writer they are handed;
// an observer that hides it downgrades every stream to byte-idle guarding.
func TestStreamCutObserver_ForwardsOutputProgressArming(t *testing.T) {
	armer := &armerRecorder{ResponseRecorder: httptest.NewRecorder()}
	attempt := newStreamCutObserver(nil).attach(armer)

	arm, ok := attempt.(providers.OutputProgressArmer)
	require.True(t, ok, "the attempt writer must stay an OutputProgressArmer")
	assert.True(t, arm.ArmOutputProgress(func() {}))
	assert.Equal(t, 1, armer.marks)

	plain := newStreamCutObserver(nil).attach(httptest.NewRecorder())
	assert.False(t, plain.(providers.OutputProgressArmer).ArmOutputProgress(func() {}),
		"a writer that cannot distinguish output frames must not claim to be armed")
}

// A turn that never cut contributes nothing to the completion line, and only
// the first (unsynthesized) error describes the cut.
func TestStreamCutObserver_FirstCutWins(t *testing.T) {
	obs := newStreamCutObserver(nil)
	obs.attach(httptest.NewRecorder())
	assert.Nil(t, obs.completionLogFields())

	obs.noteCut(context.Canceled)
	obs.noteCut(&providers.UpstreamStatusError{Status: 502})

	fields := obs.completionLogFields()
	require.Len(t, fields, 16)
	assert.Equal(t, string(streamFailureClientCanceled), fields[9])
	assert.Equal(t, false, fields[15], "the first cut's retryability must survive the synthesized 502")
}
