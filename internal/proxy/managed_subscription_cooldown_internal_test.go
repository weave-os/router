package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

// cooldownRecorder is a Leaser that keeps the reset time each cooldown was given.
type cooldownRecorder struct {
	scriptedSubscriptionLeaser
	resetAt []time.Time
}

func (c *cooldownRecorder) Cooldown(ctx context.Context, owner auth.SubscriptionOwner, provider subscriptions.Provider, accountID string, resetAt time.Time) error {
	c.resetAt = append(c.resetAt, resetAt)
	return c.scriptedSubscriptionLeaser.Cooldown(ctx, owner, provider, accountID, resetAt)
}

// upstream429 serves a 429 with the given headers from a stub upstream and
// returns the error the provider layer builds from it.
func stubUpstream429(t *testing.T, headers map[string]string) error {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, value := range headers {
			w.Header().Set(name, value)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your rate limit."}}`))
	}))
	t.Cleanup(stub.Close)
	resp, err := http.Post(stub.URL, "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return &providers.UpstreamErrorResponse{Status: resp.StatusCode, Headers: resp.Header, Body: body}
}

func unixAt(at time.Time) string { return strconv.FormatInt(at.Unix(), 10) }

func TestManagedSubscriptionCooldownFollowsStatedReset(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 14, 0, 0, time.UTC)
	fiveHourReset := now.Add(90 * time.Minute)
	weeklyReset := now.Add(38 * time.Hour)

	cases := []struct {
		name    string
		headers map[string]string
		want    time.Time
	}{
		{
			name: "five-hour window rejected, long Retry-After and weekly reset ignored",
			headers: map[string]string{
				"Retry-After":                                strconv.Itoa(int((38 * time.Hour).Seconds())),
				"anthropic-ratelimit-unified-status":         "rejected",
				"anthropic-ratelimit-unified-5h-status":      "rejected",
				"anthropic-ratelimit-unified-5h-utilization": "1.0",
				"anthropic-ratelimit-unified-5h-reset":       unixAt(fiveHourReset),
				"anthropic-ratelimit-unified-7d-status":      "allowed_warning",
				"anthropic-ratelimit-unified-7d-utilization": "0.06",
				"anthropic-ratelimit-unified-7d-reset":       unixAt(weeklyReset),
				"anthropic-ratelimit-unified-reset":          unixAt(weeklyReset),
			},
			want: fiveHourReset,
		},
		{
			name: "weekly window rejected, cooldown lasts to the weekly reset",
			headers: map[string]string{
				"anthropic-ratelimit-unified-status":         "rejected",
				"anthropic-ratelimit-unified-5h-status":      "allowed",
				"anthropic-ratelimit-unified-5h-utilization": "0.0",
				"anthropic-ratelimit-unified-5h-reset":       unixAt(fiveHourReset),
				"anthropic-ratelimit-unified-7d-status":      "rejected",
				"anthropic-ratelimit-unified-7d-utilization": "1.0",
				"anthropic-ratelimit-unified-7d-reset":       unixAt(weeklyReset),
			},
			want: weeklyReset,
		},
		{
			name: "no window rejected, long Retry-After is capped at the probe interval",
			headers: map[string]string{
				"Retry-After":                                strconv.Itoa(int((38 * time.Hour).Seconds())),
				"anthropic-ratelimit-unified-status":         "allowed",
				"anthropic-ratelimit-unified-7d-status":      "allowed",
				"anthropic-ratelimit-unified-7d-utilization": "0.06",
				"anthropic-ratelimit-unified-7d-reset":       unixAt(weeklyReset),
			},
			want: now.Add(subscriptionProbeInterval),
		},
		{
			name:    "Retry-After alone is honoured",
			headers: map[string]string{"Retry-After": "30"},
			want:    now.Add(30 * time.Second),
		},
		{
			name: "empty API-key bucket states its own reset",
			headers: map[string]string{
				"anthropic-ratelimit-tokens-remaining": "0",
				"anthropic-ratelimit-tokens-reset":     fiveHourReset.Format(time.RFC3339),
			},
			want: fiveHourReset,
		},
		{
			name:    "no stated reset probes after the interval, not a window length",
			headers: map[string]string{},
			want:    now.Add(subscriptionProbeInterval),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, managedSubscriptionResetAt(stubUpstream429(t, tc.headers), now))
		})
	}
}

func TestRecordManagedSubscriptionFailurePersistsStatedReset(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 14, 0, 0, time.UTC)
	fiveHourReset := now.Add(90 * time.Minute)
	leaser := &cooldownRecorder{}
	svc := &Service{now: func() time.Time { return now }}
	svc.WithManagedSubscriptions(leaser)

	err := stubUpstream429(t, map[string]string{
		"Retry-After":                                "136800",
		"anthropic-ratelimit-unified-status":         "rejected",
		"anthropic-ratelimit-unified-5h-status":      "rejected",
		"anthropic-ratelimit-unified-5h-utilization": "1.0",
		"anthropic-ratelimit-unified-5h-reset":       unixAt(fiveHourReset),
		"anthropic-ratelimit-unified-7d-status":      "allowed",
		"anthropic-ratelimit-unified-7d-utilization": "0.06",
		"anthropic-ratelimit-unified-7d-reset":       unixAt(now.Add(38 * time.Hour)),
	})
	rotate := svc.recordManagedSubscriptionFailure(managedSubscriptionTestContext(), providers.ProviderAnthropic, "claude-haiku-4-5",
		subscriptions.Lease{AccountID: "opaque-a", AccessToken: "token-a"}, err)

	require.True(t, rotate)
	require.Equal(t, []string{"opaque-a"}, leaser.cooldownIDs)
	require.Equal(t, []time.Time{fiveHourReset}, leaser.resetAt)
}
