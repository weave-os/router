package sse_test

import (
	"bytes"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/sse"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pingFrame     = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	testKeepalive = 40 * time.Millisecond
)

// syncRecorder is a goroutine-safe http.ResponseWriter. httptest.ResponseRecorder
// is not: the keepalive writes from its own goroutine while the test reads.
type syncRecorder struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	hdr      http.Header
	code     int
	flushes  int
	writeErr error
}

func newSyncRecorder() *syncRecorder { return &syncRecorder{hdr: make(http.Header)} }

func (s *syncRecorder) Header() http.Header { return s.hdr }

func (s *syncRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = code
}

func (s *syncRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.buf.Write(p)
}

func (s *syncRecorder) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes++
}

func (s *syncRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *syncRecorder) flushCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushes
}

func (s *syncRecorder) setWriteErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeErr = err
}

func pings(body string) int { return strings.Count(body, pingFrame) }

// A stream that goes quiet after a complete record must get keepalives, or the
// client's byte watchdog kills a turn that is still healthy upstream.
func TestKeepaliveWriter_EmitsAfterSilence(t *testing.T) {
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), testKeepalive)
	defer k.Close()

	_, err := k.Write([]byte("event: message_start\ndata: {}\n\n"))
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return pings(rec.body()) >= 2 },
		2*time.Second, 5*time.Millisecond, "silent stream must keep receiving keepalives")
	assert.True(t, strings.HasPrefix(rec.body(), "event: message_start\ndata: {}\n\n"),
		"keepalives must not precede the real stream")
}

// Arms on first byte so a keepalive cannot commit a response the router
// still wants to retry on another provider.
func TestKeepaliveWriter_SilentUntilFirstWrite(t *testing.T) {
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), testKeepalive)
	defer k.Close()

	k.WriteHeader(http.StatusOK)
	time.Sleep(6 * testKeepalive)

	assert.Empty(t, rec.body(), "unarmed writer must emit nothing")
}

// Injecting between the halves of a record would corrupt the event, so a write
// that does not end on a record boundary must suppress keepalives until one does.
func TestKeepaliveWriter_NeverSplitsARecord(t *testing.T) {
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), testKeepalive)
	defer k.Close()

	_, err := k.Write([]byte("event: content_block_delta\ndata: {\"partial\":"))
	require.NoError(t, err)
	time.Sleep(6 * testKeepalive)
	require.Zero(t, pings(rec.body()), "must not inject inside a partial record")

	_, err = k.Write([]byte("true}\n\n"))
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return pings(rec.body()) >= 1 },
		2*time.Second, 5*time.Millisecond, "keepalives must resume once the record closes")
	assert.True(t, strings.HasPrefix(rec.body(),
		"event: content_block_delta\ndata: {\"partial\":true}\n\n"),
		"the record must be reassembled intact")
}

// SSE terminators are CRLF or LF and can straddle two writes; both shapes
// silently disabled keepalives when the boundary check only matched "\n\n".
func TestKeepaliveWriter_RecognizesEveryRecordSeparator(t *testing.T) {
	for _, tc := range []struct {
		name   string
		writes []string
		want   bool
	}{
		{"lf", []string{"event: a\ndata: {}\n\n"}, true},
		{"crlf", []string{"event: a\r\ndata: {}\r\n\r\n"}, true},
		{"mixed", []string{"event: a\ndata: {}\n\r\n"}, true},
		{"separator split across writes", []string{"event: a\ndata: {}\n", "\n"}, true},
		{"record split mid-field", []string{"event: a\ndata: {", "}\n\n"}, true},
		{"single terminator is not a blank line", []string{"data: {}\r\n"}, false},
		{"trailing lone CR may be half a CRLF", []string{"data: {}\n\r"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newSyncRecorder()
			k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), testKeepalive)
			defer k.Close()

			for _, w := range tc.writes {
				_, err := k.Write([]byte(w))
				require.NoError(t, err)
			}

			got := waitFor(func() bool { return pings(rec.body()) >= 1 })
			assert.Equal(t, tc.want, got)
		})
	}
}

// A stream that is actively delivering content needs no padding.
func TestKeepaliveWriter_ActiveStreamNotPadded(t *testing.T) {
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), 200*time.Millisecond)
	defer k.Close()

	for range 10 {
		_, err := k.Write([]byte("event: content_block_delta\ndata: {}\n\n"))
		require.NoError(t, err)
		time.Sleep(20 * time.Millisecond)
	}

	assert.Zero(t, pings(rec.body()), "an active stream must not be padded")
}

// Close must stop emission before the handler returns, or a keepalive could be
// written after the response is finished.
func TestKeepaliveWriter_CloseStopsEmission(t *testing.T) {
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), testKeepalive)

	_, err := k.Write([]byte("event: message_start\ndata: {}\n\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return pings(rec.body()) >= 1 },
		2*time.Second, 5*time.Millisecond)

	k.Close()
	settled := pings(rec.body())
	time.Sleep(6 * testKeepalive)

	assert.Equal(t, settled, pings(rec.body()), "no keepalive may follow Close")
}

func TestKeepaliveWriter_CloseIsIdempotentAndSafeUnarmed(t *testing.T) {
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), testKeepalive)

	assert.NotPanics(t, k.Close, "closing an unarmed writer must be safe")
	assert.NotPanics(t, k.Close, "Close must be idempotent")
	assert.Empty(t, rec.body())
}

// A non-positive interval is the kill switch: the writer stays transparent.
func TestKeepaliveWriter_DisabledByNonPositiveInterval(t *testing.T) {
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), 0)
	defer k.Close()

	_, err := k.Write([]byte("event: message_start\ndata: {}\n\n"))
	require.NoError(t, err)
	time.Sleep(6 * testKeepalive)

	assert.Zero(t, pings(rec.body()), "interval <= 0 must disable keepalives")
}

// waitFor polls on the calling goroutine; testify.Eventually evaluates in a
// fresh goroutine, which perturbs the goroutine count the leak test measures.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// A write error must reap the goroutine on its own; otherwise every client
// disconnect leaks a ticker for the process lifetime.
func TestKeepaliveWriter_WriteErrorReapsGoroutine(t *testing.T) {
	baseline := runtime.NumGoroutine()
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), testKeepalive)
	defer k.Close()

	_, err := k.Write([]byte("event: message_start\ndata: {}\n\n"))
	require.NoError(t, err)
	require.True(t, waitFor(func() bool { return pings(rec.body()) >= 1 }))
	require.Greater(t, runtime.NumGoroutine(), baseline, "loop must be running")

	rec.setWriteErr(errors.New("connection reset"))

	assert.True(t, waitFor(func() bool { return runtime.NumGoroutine() <= baseline }),
		"the keepalive goroutine must exit on write error, not linger until Close")
}

// A dead client socket must not spin the keepalive goroutine forever.
func TestKeepaliveWriter_StopsOnWriteError(t *testing.T) {
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), testKeepalive)
	defer k.Close()

	_, err := k.Write([]byte("event: message_start\ndata: {}\n\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return pings(rec.body()) >= 1 },
		2*time.Second, 5*time.Millisecond)

	rec.setWriteErr(errors.New("connection reset"))
	time.Sleep(4 * testKeepalive)
	rec.setWriteErr(nil)

	settled := pings(rec.body())
	time.Sleep(6 * testKeepalive)
	assert.Equal(t, settled, pings(rec.body()), "keepalive must stop after a write error")
}

func TestKeepaliveWriter_PassesThroughHeaderStatusAndFlush(t *testing.T) {
	rec := newSyncRecorder()
	k := sse.NewKeepaliveWriter(rec, []byte(pingFrame), testKeepalive)
	defer k.Close()

	k.Header().Set("Content-Type", "text/event-stream")
	k.WriteHeader(http.StatusOK)
	k.Flush()

	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, http.StatusOK, rec.code)
	assert.Equal(t, 1, rec.flushCount())
}
