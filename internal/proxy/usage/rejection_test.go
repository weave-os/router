package usage

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseUnifiedRejectionIgnoresWindowsThatDidNotRefuse(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 14, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	h.Set("anthropic-ratelimit-unified-5h-reset", "2026-10-03T19:44:00Z")
	h.Set("anthropic-ratelimit-unified-7d-status", "allowed_warning")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.81")
	h.Set("anthropic-ratelimit-unified-7d-reset", "2026-10-08T00:00:00Z")

	got := ParseUnifiedRejection(h, now)

	assert.True(t, got.Present)
	assert.Equal(t, time.Date(2026, 10, 3, 19, 44, 0, 0, time.UTC), got.ResetAt)
}

func TestParseUnifiedRejectionTakesLatestRejectedReset(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 14, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
	h.Set("anthropic-ratelimit-unified-5h-reset", "1791100800")
	h.Set("anthropic-ratelimit-unified-7d-status", "rejected")
	h.Set("anthropic-ratelimit-unified-7d-reset", "1791417600")

	got := ParseUnifiedRejection(h, now)

	assert.Equal(t, time.Unix(1791417600, 0).UTC(), got.ResetAt)
}

func TestParseUnifiedRejectionAbsentHeaders(t *testing.T) {
	got := ParseUnifiedRejection(http.Header{"Retry-After": []string{"30"}}, time.Now())
	assert.False(t, got.Present)
	assert.True(t, got.ResetAt.IsZero())
}

func TestParseUnifiedRejectionOverallResetWithoutWindowBreakdown(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 14, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-status", "rejected")
	h.Set("anthropic-ratelimit-unified-reset", "2026-10-03T20:00:00Z")

	got := ParseUnifiedRejection(h, now)

	assert.True(t, got.Present)
	assert.Equal(t, time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC), got.ResetAt)
}
