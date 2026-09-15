package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
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
		"stream_upstream_blocks_completed", 0,
		"stream_upstream_output_block_started", false,
	}, obs.completionLogFields())
	assert.Equal(t,
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n"+
			"event: content_block_delta\ndata: {\"index\":0}\n\n"+
			"event: ping\ndata: {}\n\n"+
			"event: error\ndata: {\"type\":\"error\"}\n\n",
		rec.Body.String(), "the observer must pass every byte through unchanged")
}

// A cut is client-recoverable only while nothing has become final on the
// wire: the snapshot must say how many blocks closed and whether a
// non-thinking block had opened, and a fresh attempt must start from zero.
func TestStreamCutObserver_TracksCompletedAndOutputBlocks(t *testing.T) {
	const thinkingOnly = "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hm\"}}\n\n"
	const closeThinkingOpenText = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"

	fields := func(obs *streamCutObserver) (completed int, outputStarted bool) {
		logged := obs.completionLogFields()
		for i := 0; i+1 < len(logged); i += 2 {
			switch logged[i] {
			case "stream_upstream_blocks_completed":
				completed = logged[i+1].(int)
			case "stream_upstream_output_block_started":
				outputStarted = logged[i+1].(bool)
			}
		}
		return completed, outputStarted
	}

	t.Run("thinking-only stream", func(t *testing.T) {
		obs := newStreamCutObserver(nil)
		attempt := obs.attach(httptest.NewRecorder())
		_, err := attempt.Write([]byte(thinkingOnly))
		require.NoError(t, err)
		obs.noteCut(providers.ErrUpstreamIdleTimeout)
		completed, outputStarted := fields(obs)
		assert.Equal(t, 0, completed)
		assert.False(t, outputStarted)
	})

	t.Run("thinking closed and text opened", func(t *testing.T) {
		obs := newStreamCutObserver(nil)
		attempt := obs.attach(httptest.NewRecorder())
		_, err := attempt.Write([]byte(thinkingOnly))
		require.NoError(t, err)
		_, err = attempt.Write([]byte(closeThinkingOpenText))
		require.NoError(t, err)
		obs.noteCut(providers.ErrUpstreamIdleTimeout)
		completed, outputStarted := fields(obs)
		assert.Equal(t, 1, completed)
		assert.True(t, outputStarted)
	})

	t.Run("redacted thinking is not output", func(t *testing.T) {
		obs := newStreamCutObserver(nil)
		attempt := obs.attach(httptest.NewRecorder())
		_, err := attempt.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"x\"}}\n\n"))
		require.NoError(t, err)
		obs.noteCut(providers.ErrUpstreamIdleTimeout)
		_, outputStarted := fields(obs)
		assert.False(t, outputStarted)
	})

	t.Run("a new attempt starts from zero", func(t *testing.T) {
		obs := newStreamCutObserver(nil)
		first := obs.attach(httptest.NewRecorder())
		_, err := first.Write([]byte(thinkingOnly + closeThinkingOpenText))
		require.NoError(t, err)
		second := obs.attach(httptest.NewRecorder())
		_, err = second.Write([]byte(thinkingOnly))
		require.NoError(t, err)
		obs.noteCut(providers.ErrUpstreamIdleTimeout)
		completed, outputStarted := fields(obs)
		assert.Equal(t, 0, completed)
		assert.False(t, outputStarted)
	})
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
	require.Len(t, fields, 20)
	assert.Equal(t, string(streamFailureClientCanceled), fields[9])
	assert.Equal(t, false, fields[15], "the first cut's retryability must survive the synthesized 502")
}

type reasoningArmerRecorder struct {
	*httptest.ResponseRecorder
	armed bool
	mark  func()
}

func (a *reasoningArmerRecorder) ArmReasoningProgress(mark func()) bool {
	if a.armed {
		a.mark = mark
	}
	return a.armed
}

func TestStreamCutObserver_ForwardsReasoningProgressArming(t *testing.T) {
	for _, armed := range []bool{true, false} {
		inner := &reasoningArmerRecorder{ResponseRecorder: httptest.NewRecorder(), armed: armed}
		writer := newStreamCutObserver(nil).attach(inner)
		marks := 0
		assert.Equal(t, armed, writer.(providers.ReasoningProgressArmer).ArmReasoningProgress(func() { marks++ }))
		if armed {
			require.NotNil(t, inner.mark)
			inner.mark()
			assert.Equal(t, 1, marks)
		} else {
			assert.Nil(t, inner.mark)
		}
	}
	legacy := newStreamCutObserver(nil).attach(&armerRecorder{ResponseRecorder: httptest.NewRecorder()})
	assert.False(t, legacy.(providers.ReasoningProgressArmer).ArmReasoningProgress(func() {}))
}

// An Anthropic turn that closes a thinking block, then opens and closes a text
// block: nine upstream frames, two completed blocks, output started.
const streamCutFixture = "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hm\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// streamCutWire is what the observer has recorded about the upstream frames.
type streamCutWire struct {
	frames             int
	lastEvent          string
	blocksCompleted    int
	outputBlockStarted bool
}

func wireOf(o *streamCutObserver) streamCutWire {
	return streamCutWire{frames: o.frames, lastEvent: o.lastEvent, blocksCompleted: o.blocksCompleted, outputBlockStarted: o.outputBlockStarted}
}

// streamCutLargeFixture makes the text delta far larger than a provider read
// so one frame spans many writes before the stream closes.
func streamCutLargeFixture() string {
	return strings.Replace(streamCutFixture, `"text":"hello"`, `"text":"`+strings.Repeat("h", 64*1024)+`"`, 1)
}

// However the upstream's bytes are split across writes, the observer must
// describe the wire exactly as a single write of the same bytes would, after
// each write boundary and at the end.
func TestStreamCutObserver_FragmentationMatchesSingleWrite(t *testing.T) {
	fixtures := []struct {
		name string
		body string
	}{
		{name: "lf framing", body: streamCutFixture},
		{name: "crlf framing", body: strings.ReplaceAll(streamCutFixture, "\n", "\r\n")},
		{name: "frame spanning many writes", body: streamCutLargeFixture()},
	}
	prefixWire := func(t *testing.T, prefix string) streamCutWire {
		t.Helper()
		obs := newStreamCutObserver(nil)
		writeChunks(t, obs.attach(httptest.NewRecorder()), []string{prefix})
		return wireOf(obs)
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			oracle := newStreamCutObserver(nil)
			writeChunks(t, oracle.attach(httptest.NewRecorder()), []string{fx.body})
			want := wireOf(oracle)
			require.Equal(t, streamCutWire{frames: 9, lastEvent: "message_stop", blocksCompleted: 2, outputBlockStarted: true}, want)

			for _, size := range chunkSizesFor(fx.body) {
				rec := httptest.NewRecorder()
				obs := newStreamCutObserver(nil)
				writeChunks(t, obs.attach(rec), chunkEvery(fx.body, size))
				assert.Equal(t, want, wireOf(obs), "chunk size %d", size)
				assert.Equal(t, fx.body, rec.Body.String(), "chunk size %d", size)
			}

			if len(fx.body) > largeFixtureBytes {
				return
			}
			for i := 1; i < len(fx.body); i++ {
				rec := httptest.NewRecorder()
				obs := newStreamCutObserver(nil)
				attempt := obs.attach(rec)
				writeChunks(t, attempt, []string{fx.body[:i]})
				require.Equal(t, prefixWire(t, fx.body[:i]), wireOf(obs), "state after the first write, split at %d", i)
				writeChunks(t, attempt, []string{fx.body[i:]})
				require.Equal(t, want, wireOf(obs), "split at %d", i)
				require.Equal(t, fx.body, rec.Body.String(), "split at %d", i)
			}
		})
	}
}

// A failed attempt can end mid-frame. The next attempt's first frame must be
// framed from its own first byte, not from wherever the abandoned tail's scan
// had reached.
func TestStreamCutObserver_ReattachAfterIncompleteTailStartsFresh(t *testing.T) {
	obs := newStreamCutObserver(nil)
	first := obs.attach(httptest.NewRecorder())
	writeChunks(t, first, []string{"event: content_block_delta\ndata: {\"text\":\"" + strings.Repeat("y", 300)})
	require.Equal(t, streamCutWire{}, wireOf(obs), "an unterminated frame is not a frame")

	rec := httptest.NewRecorder()
	second := obs.attach(rec)
	writeChunks(t, second, []string{"event: ping\ndata: {}\n\n"})

	assert.Equal(t, streamCutWire{frames: 1, lastEvent: "ping"}, wireOf(obs))
	assert.Equal(t, "event: ping\ndata: {}\n\n", rec.Body.String())
}

// A partial frame that outgrows the carry cap is dropped rather than retained;
// the frame that follows is still counted whole, whether the cap was crossed in
// one write or after the tail had already been carried.
func TestStreamCutObserver_TailCapDiscardResyncsOnNextFrame(t *testing.T) {
	const ping = "event: ping\ndata: {}\n\n"
	cases := []struct {
		name   string
		writes []string
	}{
		{name: "cap crossed in one write", writes: []string{strings.Repeat("z", streamCutCarryCap+1), ping}},
		{name: "cap crossed by a carried tail", writes: []string{strings.Repeat("z", streamCutCarryCap/2), strings.Repeat("z", streamCutCarryCap/2+1), ping}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			obs := newStreamCutObserver(nil)
			writeChunks(t, obs.attach(rec), tc.writes)

			assert.Equal(t, streamCutWire{frames: 1, lastEvent: "ping"}, wireOf(obs))
			assert.Empty(t, obs.carry)
			assert.Equal(t, strings.Join(tc.writes, ""), rec.Body.String(), "the observer must pass every byte through unchanged")
		})
	}

	t.Run("a tail under the cap is kept", func(t *testing.T) {
		obs := newStreamCutObserver(nil)
		frame := "event: content_block_delta\ndata: {\"text\":\"" + strings.Repeat("k", streamCutCarryCap-64) + "\"}\n\n"
		writeChunks(t, obs.attach(httptest.NewRecorder()), []string{frame[:streamCutCarryCap/2], frame[streamCutCarryCap/2:]})
		assert.Equal(t, streamCutWire{frames: 1, lastEvent: "content_block_delta"}, wireOf(obs))
	})
}
