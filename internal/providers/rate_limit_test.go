package providers_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func TestIsUpstreamRateLimited(t *testing.T) {
	buffered429 := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}
	assert.True(t, providers.IsUpstreamRateLimited(buffered429))
	assert.True(t, providers.IsUpstreamRateLimited(fmt.Errorf("upstream: %w", buffered429)), "wrapped")
	assert.False(t, providers.IsUpstreamRateLimited(&providers.UpstreamErrorResponse{Status: http.StatusBadGateway}))
	assert.False(t, providers.IsUpstreamRateLimited(&providers.UpstreamErrorResponse{Status: 529}), "overload is not throttling")
	assert.False(t, providers.IsUpstreamRateLimited(errors.New("connection reset")))
	assert.False(t, providers.IsUpstreamRateLimited(context.Canceled))
	assert.False(t, providers.IsUpstreamRateLimited(nil))
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	with := func(value string) error {
		h := http.Header{}
		if value != "" {
			h.Set("Retry-After", value)
		}
		return fmt.Errorf("wrapped: %w", &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Headers: h})
	}

	cases := []struct {
		name      string
		err       error
		wantDelay time.Duration
		wantOK    bool
	}{
		{name: "seconds", err: with("7"), wantDelay: 7 * time.Second, wantOK: true},
		{name: "seconds with whitespace", err: with(" 7 "), wantDelay: 7 * time.Second, wantOK: true},
		{name: "zero seconds", err: with("0"), wantDelay: 0, wantOK: true},
		{name: "negative seconds", err: with("-1"), wantOK: false},
		{name: "seconds beyond time.Duration saturate", err: with("100000000000"), wantDelay: math.MaxInt64, wantOK: true},
		{name: "http-date ahead", err: with(now.Add(90 * time.Second).Format(http.TimeFormat)), wantDelay: 90 * time.Second, wantOK: true},
		{name: "http-date in the past", err: with(now.Add(-time.Minute).Format(http.TimeFormat)), wantDelay: 0, wantOK: true},
		{name: "absent", err: with(""), wantOK: false},
		{name: "no headers at all", err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}, wantOK: false},
		{name: "malformed", err: with("soon"), wantOK: false},
		{name: "fractional seconds are not delay-seconds", err: with("1.5"), wantOK: false},
		{name: "not a buffered upstream error", err: errors.New("connection reset"), wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			delay, ok := providers.RetryAfter(tc.err, now)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantDelay, delay)
		})
	}
}
