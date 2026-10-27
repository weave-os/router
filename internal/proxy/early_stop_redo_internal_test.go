package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/catalog"
)

const earlyStopStoppedStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestEarlyStopGate_WithholdsAStopSplitAcrossWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec)
	g.observe(true)
	for i := 0; i < len(earlyStopStoppedStream); i += 7 {
		end := min(i+7, len(earlyStopStoppedStream))
		_, err := g.Write([]byte(earlyStopStoppedStream[i:end]))
		require.NoError(t, err)
	}
	assert.True(t, g.earlyStop())
	assert.True(t, g.withheld())
	assert.Empty(t, rec.Body.String(), "nothing reaches the client while withheld")
	_, body := g.takeWithheld()
	assert.Equal(t, earlyStopStoppedStream, string(body))
}

func TestEarlyStopGate_ReleasesAtFirstToolUse(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec)
	g.observe(true)
	_, _ = g.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
	assert.Empty(t, rec.Body.String())
	_, _ = g.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"Bash\"}}\n\n"))
	assert.False(t, g.withheld())
	assert.Contains(t, rec.Body.String(), "message_start")
	assert.Contains(t, rec.Body.String(), "tool_use")
	assert.False(t, g.earlyStop())
}

func TestEarlyStopGate_ReleasesAfterHoldMaxWithoutAnotherWrite(t *testing.T) {
	rec := newSyncRecorder()
	g := newEarlyStopGate(rec)
	g.holdMax = 10 * time.Millisecond
	g.observe(true)
	_, _ = g.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
	require.Eventually(t, func() bool { return !g.withheld() }, time.Second, 5*time.Millisecond,
		"a silent upstream must not hold the client past holdMax")
	assert.Equal(t, "hold_timeout", g.releaseReason())
	assert.Contains(t, rec.body(), "message_start")
}

func TestEarlyStopGate_ShadowPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec)
	g.observe(false)
	_, _ = g.Write([]byte(earlyStopStoppedStream))
	assert.Equal(t, earlyStopStoppedStream, rec.Body.String())
	assert.True(t, g.earlyStop())
	assert.False(t, g.withheld())
}

func TestEarlyStopGate_DiscardStartsTheNextAttemptClean(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec)
	g.observe(true)
	_, _ = g.Write([]byte(earlyStopStoppedStream))
	g.resetAttempt()
	assert.False(t, g.earlyStop())
	assert.True(t, g.withheld())
	_, body := g.takeWithheld()
	assert.Empty(t, body)
}

func TestHigherTierCandidates_TopTierFirst(t *testing.T) {
	got := higherTierCandidates(catalog.TierLow,
		[]string{"claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-7"},
		map[string]float32{"claude-opus-4-6": 0.9, "claude-opus-4-7": 0.4, "claude-sonnet-4-6": 0.99})
	assert.Equal(t, []string{"claude-opus-4-6", "claude-opus-4-7", "claude-sonnet-4-6"}, got)
	assert.Empty(t, higherTierCandidates(catalog.TierHigh, []string{"claude-haiku-4-5", "claude-opus-4-7"}, nil))
}

func TestLowerTierCandidates(t *testing.T) {
	assert.Equal(t, []string{"claude-haiku-4-5", "claude-sonnet-4-6"},
		lowerTierCandidates("claude-opus-4-7", []string{"claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-7"}))
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
