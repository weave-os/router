package proxy

import (
	"net/http/httptest"
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
	g := newEarlyStopGate(rec, time.Now)
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
	g := newEarlyStopGate(rec, time.Now)
	g.observe(true)
	_, _ = g.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
	assert.Empty(t, rec.Body.String())
	_, _ = g.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"Bash\"}}\n\n"))
	assert.False(t, g.withheld())
	assert.Contains(t, rec.Body.String(), "message_start")
	assert.Contains(t, rec.Body.String(), "tool_use")
	assert.False(t, g.earlyStop())
}

func TestEarlyStopGate_ReleasesAfterHoldMax(t *testing.T) {
	rec := httptest.NewRecorder()
	now := time.Unix(0, 0)
	g := newEarlyStopGate(rec, func() time.Time { return now })
	g.observe(true)
	_, _ = g.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
	now = now.Add(earlyStopHoldMax)
	_, _ = g.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
	assert.False(t, g.withheld(), "a long hold gives up the redo rather than risk the client timeout")
	assert.Equal(t, "hold_timeout", g.releaseCause)
	assert.Contains(t, rec.Body.String(), "message_start")
}

func TestEarlyStopGate_ShadowPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec, time.Now)
	g.observe(false)
	_, _ = g.Write([]byte(earlyStopStoppedStream))
	assert.Equal(t, earlyStopStoppedStream, rec.Body.String())
	assert.True(t, g.earlyStop())
	assert.False(t, g.withheld())
}

func TestEarlyStopGate_DiscardStartsTheNextAttemptClean(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec, time.Now)
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
