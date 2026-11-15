package dispatch_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
)

const earlyStopStoppedStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestEarlyStopGate_WithholdsAStopSplitAcrossWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	g := dispatch.NewEarlyStopGate(rec, slog.Default())
	g.Observe(true)
	for i := 0; i < len(earlyStopStoppedStream); i += 7 {
		end := min(i+7, len(earlyStopStoppedStream))
		_, err := g.Write([]byte(earlyStopStoppedStream[i:end]))
		require.NoError(t, err)
	}
	assert.True(t, g.EarlyStop())
	assert.True(t, g.Withheld())
	assert.Empty(t, rec.Body.String(), "nothing reaches the client while withheld")
	_, body := g.TakeWithheld()
	assert.Equal(t, earlyStopStoppedStream, string(body))
}

func TestEarlyStopGate_ReleasesAtFirstToolUse(t *testing.T) {
	rec := httptest.NewRecorder()
	g := dispatch.NewEarlyStopGate(rec, slog.Default())
	g.Observe(true)
	_, _ = g.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
	assert.Empty(t, rec.Body.String())
	_, _ = g.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"Bash\"}}\n\n"))
	assert.False(t, g.Withheld())
	assert.Contains(t, rec.Body.String(), "message_start")
	assert.Contains(t, rec.Body.String(), "tool_use")
	assert.False(t, g.EarlyStop())
}

func TestEarlyStopGate_ReleasesAfterHoldMaxWithoutAnotherWrite(t *testing.T) {
	rec := newSyncRecorder()
	g := dispatch.NewEarlyStopGate(rec, slog.Default())
	g.SetHoldMax(10 * time.Millisecond)
	g.Observe(true)
	_, _ = g.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
	require.Eventually(t, func() bool { return !g.Withheld() }, time.Second, 5*time.Millisecond,
		"a silent upstream must not hold the client past holdMax")
	assert.Equal(t, "hold_timeout", g.ReleaseReason())
	assert.Contains(t, rec.body(), "message_start")
}

func TestEarlyStopGate_ShadowPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	g := dispatch.NewEarlyStopGate(rec, slog.Default())
	g.Observe(false)
	_, _ = g.Write([]byte(earlyStopStoppedStream))
	assert.Equal(t, earlyStopStoppedStream, rec.Body.String())
	assert.True(t, g.EarlyStop())
	assert.False(t, g.Withheld())
}

func TestEarlyStopGate_DiscardStartsTheNextAttemptClean(t *testing.T) {
	rec := httptest.NewRecorder()
	g := dispatch.NewEarlyStopGate(rec, slog.Default())
	g.Observe(true)
	_, _ = g.Write([]byte(earlyStopStoppedStream))
	g.ResetAttempt()
	assert.False(t, g.EarlyStop())
	assert.True(t, g.Withheld())
	_, body := g.TakeWithheld()
	assert.Empty(t, body)
}

// syncRecorder is an httptest recorder safe to read while the hold timer writes.
type syncRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newSyncRecorder() *syncRecorder { return &syncRecorder{rec: httptest.NewRecorder()} }

func (r *syncRecorder) Header() http.Header { return r.rec.Header() }

func (r *syncRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.WriteHeader(status)
}

func (r *syncRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Write(p)
}

func (r *syncRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Body.String()
}
