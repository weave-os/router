package dispatch

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// headerTimeoutError mirrors net/http's HTTP/2 response-header timeout: a
// net.Error reporting Timeout() with no context.DeadlineExceeded in its chain.
type headerTimeoutError struct{ msg string }

func http2HeaderTimeout() error {
	return &url.Error{Op: "Post", URL: "https://upstream.test", Err: headerTimeoutError{msg: "http2: timeout awaiting response headers"}}
}

// Retryability describes a fresh replay of the same request, not the committed
// turn: caller-side cuts are never worth replaying, an upstream that named a
// non-retryable status has already answered, and everything else is the
// upstream's to fail again.
func TestStreamCutReplayRetryable(t *testing.T) {
	cases := []struct {
		name  string
		class StreamFailureClass
		err   error
		want  bool
	}{
		{name: "upstream eof", class: StreamFailureUpstreamEOF, err: io.ErrUnexpectedEOF, want: true},
		{name: "upstream reset", class: StreamFailureUpstreamReset, err: syscall.ECONNRESET, want: true},
		{name: "idle watchdog", class: StreamFailureIdleWatchdog, err: providers.ErrUpstreamIdleTimeout, want: true},
		{name: "stream without a terminal event", class: StreamFailureUpstreamIncomplete, err: translate.ErrStreamIncomplete, want: true},
		{name: "stream that never started", class: StreamFailureUpstreamEmpty, err: translate.ErrStreamEmpty, want: true},
		{name: "upstream response-header timeout", class: StreamFailureUpstreamTimeout, err: http2HeaderTimeout(), want: true},
		{name: "output stall watchdog", class: StreamFailureOutputStallWatchdog, err: providers.ErrUpstreamOutputStall, want: true},
		{name: "client disconnect", class: StreamFailureClientCanceled, err: context.Canceled, want: false},
		{name: "request deadline", class: StreamFailureDeadline, err: context.DeadlineExceeded, want: false},
		{name: "flushed 502", class: StreamFailureUpstreamErrorFrame, err: &providers.UpstreamStatusError{Status: 502}, want: true},
		{name: "flushed 529 overload", class: StreamFailureUpstreamErrorFrame, err: &providers.UpstreamStatusError{Status: 529}, want: true},
		{name: "flushed 400", class: StreamFailureUpstreamErrorFrame, err: &providers.UpstreamStatusError{Status: 400}, want: false},
		{name: "error event without a status", class: StreamFailureUpstreamErrorFrame, err: errors.New("stream closed"), want: true},
		{name: "unattributable transport error", class: StreamFailureOther, err: errors.New("stream closed"), want: true},
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
	obs := NewStreamCutObserver(func() time.Time { return clock })
	turn, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-8","thinking":{"type":"enabled","budget_tokens":2048},"messages":[]}`))
	require.NoError(t, err)
	obs.DescribeRequest(48213, turn)
	attempt := obs.Attach(rec)

	clock = clock.Add(2 * time.Second)
	_, err = attempt.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: {\"in"))
	require.NoError(t, err)
	clock = clock.Add(58 * time.Second)
	_, err = attempt.Write([]byte("dex\":0}\n\nevent: ping\ndata: {}\n\n"))
	require.NoError(t, err)

	clock = clock.Add(4 * time.Second)
	obs.NoteCut(io.ErrUnexpectedEOF)
	_, err = attempt.Write([]byte("event: error\ndata: {\"type\":\"error\"}\n\n"))
	require.NoError(t, err)

	assert.Equal(t, []any{
		"stream_cut_elapsed_ms", int64(64000),
		"stream_ms_since_last_upstream_frame", int64(4000),
		"stream_last_upstream_event", "ping",
		"stream_upstream_frames", 3,
		"stream_failure_class", string(StreamFailureUpstreamEOF),
		"stream_cut_request_bytes", 48213,
		"stream_cut_thinking", true,
		"stream_cut_replay_retryable", true,
		"stream_upstream_blocks_completed", 0,
		"stream_upstream_output_block_started", false,
	}, obs.CompletionLogFields())
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

	fields := func(obs *StreamCutObserver) (completed int, outputStarted bool) {
		logged := obs.CompletionLogFields()
		for i := 0; i+1 < len(logged); i += 2 {
			switch logged[i] {
			case "stream_upstream_blocks_completed":
				completed = mustAs[int](t, logged[i+1])
			case "stream_upstream_output_block_started":
				outputStarted = mustAs[bool](t, logged[i+1])
			}
		}
		return completed, outputStarted
	}

	t.Run("thinking-only stream", func(t *testing.T) {
		obs := NewStreamCutObserver(nil)
		attempt := obs.Attach(httptest.NewRecorder())
		_, err := attempt.Write([]byte(thinkingOnly))
		require.NoError(t, err)
		obs.NoteCut(providers.ErrUpstreamIdleTimeout)
		completed, outputStarted := fields(obs)
		assert.Equal(t, 0, completed)
		assert.False(t, outputStarted)
	})

	t.Run("thinking closed and text opened", func(t *testing.T) {
		obs := NewStreamCutObserver(nil)
		attempt := obs.Attach(httptest.NewRecorder())
		_, err := attempt.Write([]byte(thinkingOnly))
		require.NoError(t, err)
		_, err = attempt.Write([]byte(closeThinkingOpenText))
		require.NoError(t, err)
		obs.NoteCut(providers.ErrUpstreamIdleTimeout)
		completed, outputStarted := fields(obs)
		assert.Equal(t, 1, completed)
		assert.True(t, outputStarted)
	})

	t.Run("redacted thinking is not output", func(t *testing.T) {
		obs := NewStreamCutObserver(nil)
		attempt := obs.Attach(httptest.NewRecorder())
		_, err := attempt.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"x\"}}\n\n"))
		require.NoError(t, err)
		obs.NoteCut(providers.ErrUpstreamIdleTimeout)
		_, outputStarted := fields(obs)
		assert.False(t, outputStarted)
	})

	t.Run("a new attempt starts from zero", func(t *testing.T) {
		obs := NewStreamCutObserver(nil)
		first := obs.Attach(httptest.NewRecorder())
		_, err := first.Write([]byte(thinkingOnly + closeThinkingOpenText))
		require.NoError(t, err)
		second := obs.Attach(httptest.NewRecorder())
		_, err = second.Write([]byte(thinkingOnly))
		require.NoError(t, err)
		obs.NoteCut(providers.ErrUpstreamIdleTimeout)
		completed, outputStarted := fields(obs)
		assert.Equal(t, 0, completed)
		assert.False(t, outputStarted)
	})
}

type armerRecorder struct {
	*httptest.ResponseRecorder
	marks int
}

// The provider adapters arm the output-stall watchdog and the first-output
// latency stamp through an optional interface on the writer they are handed;
// an observer that hides it downgrades every stream to byte-idle guarding.
func TestStreamCutObserver_ForwardsOutputProgressArming(t *testing.T) {
	armer := &armerRecorder{ResponseRecorder: httptest.NewRecorder()}
	attempt := NewStreamCutObserver(nil).Attach(armer)

	arm, ok := attempt.(providers.OutputProgressArmer)
	require.True(t, ok, "the attempt writer must stay an OutputProgressArmer")
	assert.True(t, arm.ArmOutputProgress(func() {}))
	assert.Equal(t, 1, armer.marks)

	plain := NewStreamCutObserver(nil).Attach(httptest.NewRecorder())
	assert.False(t, mustAs[providers.OutputProgressArmer](t, plain).ArmOutputProgress(func() {}),
		"a writer that cannot distinguish output frames must not claim to be armed")
}

// A turn that never cut contributes nothing to the completion line, and only
// the first (unsynthesized) error describes the cut.
func TestStreamCutObserver_FirstCutWins(t *testing.T) {
	obs := NewStreamCutObserver(nil)
	obs.Attach(httptest.NewRecorder())
	assert.Nil(t, obs.CompletionLogFields())

	obs.NoteCut(context.Canceled)
	obs.NoteCut(&providers.UpstreamStatusError{Status: 502})

	fields := obs.CompletionLogFields()
	require.Len(t, fields, 20)
	assert.Equal(t, string(StreamFailureClientCanceled), fields[9])
	assert.Equal(t, false, fields[15], "the first cut's retryability must survive the synthesized 502")
}

type reasoningArmerRecorder struct {
	*httptest.ResponseRecorder
	armed bool
	mark  func()
}

func TestStreamCutObserver_ForwardsReasoningProgressArming(t *testing.T) {
	for _, armed := range []bool{true, false} {
		inner := &reasoningArmerRecorder{ResponseRecorder: httptest.NewRecorder(), armed: armed}
		writer := NewStreamCutObserver(nil).Attach(inner)
		marks := 0
		assert.Equal(t, armed, mustAs[providers.ReasoningProgressArmer](t, writer).ArmReasoningProgress(func() { marks++ }))
		if armed {
			require.NotNil(t, inner.mark)
			inner.mark()
			assert.Equal(t, 1, marks)
		} else {
			assert.Nil(t, inner.mark)
		}
	}
	legacy := NewStreamCutObserver(nil).Attach(&armerRecorder{ResponseRecorder: httptest.NewRecorder()})
	assert.False(t, mustAs[providers.ReasoningProgressArmer](t, legacy).ArmReasoningProgress(func() {}))
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

func wireOf(o *StreamCutObserver) streamCutWire {
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
		obs := NewStreamCutObserver(nil)
		writeChunks(t, obs.Attach(httptest.NewRecorder()), []string{prefix})
		return wireOf(obs)
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			oracle := NewStreamCutObserver(nil)
			writeChunks(t, oracle.Attach(httptest.NewRecorder()), []string{fx.body})
			want := wireOf(oracle)
			require.Equal(t, streamCutWire{frames: 9, lastEvent: "message_stop", blocksCompleted: 2, outputBlockStarted: true}, want)

			for _, size := range chunkSizesFor(fx.body) {
				rec := httptest.NewRecorder()
				obs := NewStreamCutObserver(nil)
				writeChunks(t, obs.Attach(rec), chunkEvery(fx.body, size))
				assert.Equal(t, want, wireOf(obs), "chunk size %d", size)
				assert.Equal(t, fx.body, rec.Body.String(), "chunk size %d", size)
			}

			if len(fx.body) > largeFixtureBytes {
				return
			}
			for i := 1; i < len(fx.body); i++ {
				rec := httptest.NewRecorder()
				obs := NewStreamCutObserver(nil)
				attempt := obs.Attach(rec)
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
	obs := NewStreamCutObserver(nil)
	first := obs.Attach(httptest.NewRecorder())
	writeChunks(t, first, []string{"event: content_block_delta\ndata: {\"text\":\"" + strings.Repeat("y", 300)})
	require.Equal(t, streamCutWire{}, wireOf(obs), "an unterminated frame is not a frame")

	rec := httptest.NewRecorder()
	second := obs.Attach(rec)
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
			obs := NewStreamCutObserver(nil)
			writeChunks(t, obs.Attach(rec), tc.writes)

			assert.Equal(t, streamCutWire{frames: 1, lastEvent: "ping"}, wireOf(obs))
			assert.Empty(t, obs.carry)
			assert.Equal(t, strings.Join(tc.writes, ""), rec.Body.String(), "the observer must pass every byte through unchanged")
		})
	}

	t.Run("a tail under the cap is kept", func(t *testing.T) {
		obs := NewStreamCutObserver(nil)
		frame := "event: content_block_delta\ndata: {\"text\":\"" + strings.Repeat("k", streamCutCarryCap-64) + "\"}\n\n"
		writeChunks(t, obs.Attach(httptest.NewRecorder()), []string{frame[:streamCutCarryCap/2], frame[streamCutCarryCap/2:]})
		assert.Equal(t, streamCutWire{frames: 1, lastEvent: "content_block_delta"}, wireOf(obs))
	})
}

func (e headerTimeoutError) Error() string { return e.msg }

func (e headerTimeoutError) Timeout() bool { return true }

func (e headerTimeoutError) Temporary() bool { return true }

func (a *armerRecorder) ArmOutputProgress(mark func()) bool {
	a.marks++
	mark()
	return true
}

func (a *reasoningArmerRecorder) ArmReasoningProgress(mark func()) bool {
	if a.armed {
		a.mark = mark
	}
	return a.armed
}

func chunkSizesFor(body string) []int {
	if len(body) > largeFixtureBytes {
		return []int{4096}
	}
	return fragmentationChunkSizes
}

// writeChunks writes each chunk as its own Write call, failing the test on the
// first error.
func writeChunks(t *testing.T, w io.Writer, chunks []string) {
	t.Helper()
	for _, chunk := range chunks {
		_, err := w.Write([]byte(chunk))
		require.NoError(t, err)
	}
}

func mustAs[T any](tb testing.TB, v any) T {
	tb.Helper()
	got, ok := v.(T)
	require.Truef(tb, ok, "value is %T, want %T", v, got)
	return got
}

// Chunk sizes every buffered SSE consumer is exercised at: one byte, odd sizes
// that land boundaries mid-line and mid-delimiter, and the provider adapters'
// read size.
var fragmentationChunkSizes = []int{1, 3, 7, 13, 4096}

// largeFixtureBytes is the size past which a fixture is fed only in the
// adapters' 4 KiB reads: byte-at-a-time writes of a large body exercise nothing
// further and make the test's runtime a function of the scanner's speed.
const largeFixtureBytes = 2048

// chunkEvery splits body into consecutive writes of at most size bytes.
func chunkEvery(body string, size int) []string {
	var chunks []string
	for start := 0; start < len(body); start += size {
		chunks = append(chunks, body[start:min(start+size, len(body))])
	}
	return chunks
}
