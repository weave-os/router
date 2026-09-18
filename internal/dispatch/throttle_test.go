package dispatch_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
)

var throttleNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func rateLimited(retryAfter string) error {
	err := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Headers: http.Header{}}
	if retryAfter != "" {
		err.Headers.Set("Retry-After", retryAfter)
	}
	return err
}

func TestThrottlePolicyRetryDelay(t *testing.T) {
	policy := dispatch.ThrottlePolicy{Now: func() time.Time { return throttleNow }}
	httpDate := throttleNow.Add(7 * time.Second).Format(http.TimeFormat)

	cases := []struct {
		name      string
		attempt   dispatch.Attempt
		err       error
		wantDelay time.Duration
		wantRetry bool
	}{
		{name: "seconds within cap", err: rateLimited("3"), wantDelay: 3 * time.Second, wantRetry: true},
		{name: "seconds at cap", err: rateLimited("10"), wantDelay: 10 * time.Second, wantRetry: true},
		{name: "seconds above cap ends same-arm retries", err: rateLimited("11"), wantDelay: 0, wantRetry: false},
		{name: "http-date within cap", err: rateLimited(httpDate), wantDelay: 7 * time.Second, wantRetry: true},
		{name: "http-date above cap", err: rateLimited(throttleNow.Add(time.Minute).Format(http.TimeFormat)), wantRetry: false},
		{name: "http-date in the past retries at once", err: rateLimited(throttleNow.Add(-time.Minute).Format(http.TimeFormat)), wantDelay: 0, wantRetry: true},
		{name: "zero seconds retries at once", err: rateLimited("0"), wantDelay: 0, wantRetry: true},
		{name: "absent header, first retry", err: rateLimited(""), wantDelay: 500 * time.Millisecond, wantRetry: true},
		{name: "absent header, second retry", attempt: dispatch.Attempt{SameBindingRetry: 1}, err: rateLimited(""), wantDelay: 1500 * time.Millisecond, wantRetry: true},
		{name: "absent header, beyond schedule holds last step", attempt: dispatch.Attempt{SameBindingRetry: 5}, err: rateLimited(""), wantDelay: 1500 * time.Millisecond, wantRetry: true},
		{name: "malformed header falls back to backoff", err: rateLimited("soon"), wantDelay: 500 * time.Millisecond, wantRetry: true},
		{name: "negative seconds fall back to backoff", err: rateLimited("-5"), wantDelay: 500 * time.Millisecond, wantRetry: true},
		{name: "non-429 keeps the executor's default", err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}, wantDelay: 250 * time.Millisecond, wantRetry: true},
		{name: "transport error keeps the executor's default", err: errors.New("connection reset"), wantDelay: 250 * time.Millisecond, wantRetry: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			delay, retry := policy.RetryDelay(tc.attempt, tc.err, 250*time.Millisecond)
			assert.Equal(t, tc.wantRetry, retry)
			assert.Equal(t, tc.wantDelay, delay)
		})
	}
}

func TestThrottlePolicyObservesHonouredRetryAfter(t *testing.T) {
	var seen []dispatch.ThrottleDecision
	policy := dispatch.ThrottlePolicy{
		Now:     func() time.Time { return throttleNow },
		Observe: func(d dispatch.ThrottleDecision) { seen = append(seen, d) },
	}

	policy.RetryDelay(dispatch.Attempt{}, rateLimited("2"), 250*time.Millisecond)
	policy.RetryDelay(dispatch.Attempt{}, rateLimited(""), 250*time.Millisecond)
	policy.RetryDelay(dispatch.Attempt{}, rateLimited("60"), 250*time.Millisecond)
	policy.RetryDelay(dispatch.Attempt{}, &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}, 250*time.Millisecond)

	require.Len(t, seen, 3, "only 429s are observed")
	assert.Equal(t, dispatch.ThrottleDecision{RetryAfter: 2 * time.Second, HasRetryAfter: true, Honoured: true, Delay: 2 * time.Second, Retry: true}, seen[0])
	assert.Equal(t, dispatch.ThrottleDecision{Delay: 500 * time.Millisecond, Retry: true}, seen[1])
	assert.Equal(t, dispatch.ThrottleDecision{RetryAfter: time.Minute, HasRetryAfter: true, Retry: false}, seen[2])
}

// The executor sleeps the policy's schedule between same-binding 429 retries
// instead of its 250/500 ms default.
func TestRunHonoursThrottleBackoffSchedule(t *testing.T) {
	fw := &fakeUpstream{errs: []error{rateLimited(""), rateLimited("")}}
	var slept []time.Duration
	exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}),
		dispatch.WithSleep(func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }),
	)
	require.NoError(t, err)

	result, err := exec.Run(context.Background(), inference.InvocationRequest{},
		fakePlan{selected: primary},
		dispatch.Transport{
			Attempt:    attemptWith([]byte(`{"model":"kimi-k2.5"}`)),
			RetryDelay: dispatch.ThrottlePolicy{}.RetryDelay,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, 3, result.Outcome.AttemptCount)
	assert.Equal(t, []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}, slept)
}

// A Retry-After within the cap is slept once; one above the cap ends the
// same-binding retries so the surface can rescue, with no sleep at all.
func TestRunHonoursRetryAfterUpToCap(t *testing.T) {
	t.Run("within cap", func(t *testing.T) {
		fw := &fakeUpstream{errs: []error{rateLimited("4")}}
		var slept []time.Duration
		exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}),
			dispatch.WithSleep(func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }),
		)
		require.NoError(t, err)

		result, err := exec.Run(context.Background(), inference.InvocationRequest{},
			fakePlan{selected: primary},
			dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`)), RetryDelay: dispatch.ThrottlePolicy{}.RetryDelay},
		)
		require.NoError(t, err)
		assert.Equal(t, 2, result.Outcome.AttemptCount)
		assert.Equal(t, []time.Duration{4 * time.Second}, slept)
	})

	t.Run("above cap", func(t *testing.T) {
		fw := &fakeUpstream{errs: []error{rateLimited("30"), nil}}
		var slept []time.Duration
		exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}),
			dispatch.WithSleep(func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }),
		)
		require.NoError(t, err)

		result, err := exec.Run(context.Background(), inference.InvocationRequest{},
			fakePlan{selected: primary},
			dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`)), RetryDelay: dispatch.ThrottlePolicy{}.RetryDelay},
		)
		require.Error(t, err)
		assert.Equal(t, 1, result.Outcome.AttemptCount, "no same-arm retry: the turn is handed to rescue")
		assert.Empty(t, slept)
		assert.Equal(t, dispatch.FailureReasonUpstreamStatus, result.Summary.FallbackReason)
	})
}

// A chosen delay that would overrun the 10 s same-binding budget ends the
// retries even though it is within the Retry-After cap on its own.
func TestRunRetryDelayRespectsSameBindingBudget(t *testing.T) {
	fw := &fakeUpstream{errs: []error{rateLimited("8"), rateLimited("8")}}
	now := throttleNow
	var slept []time.Duration
	exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}),
		dispatch.WithClock(func() time.Time { return now }),
		dispatch.WithSleep(func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			now = now.Add(d)
			return nil
		}),
	)
	require.NoError(t, err)

	result, err := exec.Run(context.Background(), inference.InvocationRequest{},
		fakePlan{selected: primary},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`)), RetryDelay: dispatch.ThrottlePolicy{}.RetryDelay},
	)
	require.Error(t, err)
	assert.Equal(t, 2, result.Outcome.AttemptCount, "8 s slept, a second 8 s would overrun the budget")
	assert.Equal(t, []time.Duration{8 * time.Second}, slept)
}

// Without RetryDelay the executor's schedule is untouched.
func TestRunWithoutRetryDelayKeepsDefaultBackoff(t *testing.T) {
	fw := &fakeUpstream{errs: []error{rateLimited("4"), rateLimited("4")}}
	var slept []time.Duration
	exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}),
		dispatch.WithSleep(func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }),
	)
	require.NoError(t, err)

	result, err := exec.Run(context.Background(), inference.InvocationRequest{},
		fakePlan{selected: primary},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`))},
	)
	require.NoError(t, err)
	assert.Equal(t, 3, result.Outcome.AttemptCount)
	assert.Equal(t, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}, slept)
}
