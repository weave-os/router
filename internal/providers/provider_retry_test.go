package providers_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
)

// TestIsRetryable_UpstreamIdleTimeout pins the classification of the SSE
// idle-watchdog sentinel: a mid-stream zero-progress stall is the upstream's
// fault and must be retryable so dispatchWithFallback can rescue the turn on
// the next attempt. Both the bare sentinel and a wrapped chain must classify,
// since adapters may annotate the error on the way out.
func TestIsRetryable_UpstreamIdleTimeout(t *testing.T) {
	assert.True(t, providers.IsRetryable(providers.ErrUpstreamIdleTimeout))
	assert.True(t, providers.IsRetryable(fmt.Errorf("stream upstream response: %w", providers.ErrUpstreamIdleTimeout)))
}

func TestIsRetryable_ResponseHeaderTimeout(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)

	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Millisecond}}
	_, err := client.Post(upstream.URL, "application/json", strings.NewReader(`{}`))

	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "timeout awaiting response headers")
	assert.True(t, providers.IsRetryable(err))
}

func TestIsRetryable_RequestDeadlineExceeded(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, strings.NewReader(`{}`))
	require.NoError(t, err)
	_, err = http.DefaultClient.Do(req)

	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, providers.IsRetryable(err))
}

// A capability rejection must be recognised from the upstream's prose, and must
// NOT be classified as retryable — re-sending the same model to a different
// provider gets the same rejection, so the only useful response is a different
// model.
func TestIsUpstreamCapabilityRejection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "together multimodal rejection",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"deepseek-ai/DeepSeek-V4-Pro is not a multimodal model"}}`,
			want:   true,
		},
		{
			name:   "does not support images",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"This model does not support image inputs."}}`,
			want:   true,
		},
		{
			name:   "case insensitive",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"Model Does Not Support Vision"}}`,
			want:   true,
		},
		{
			name:   "ordinary validation 400 is not a capability rejection",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"messages: at least one message is required"}}`,
			want:   false,
		},
		{
			name:   "same phrasing on a 500 is an outage, not a capability verdict",
			status: http.StatusInternalServerError,
			body:   `{"error":{"message":"not a multimodal model"}}`,
			want:   false,
		},
		{
			name:   "404 stays model-not-found",
			status: http.StatusNotFound,
			body:   `{"error":{"message":"model not found"}}`,
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &providers.UpstreamErrorResponse{Status: tc.status, Body: []byte(tc.body)}
			assert.Equal(t, tc.want, providers.IsUpstreamCapabilityRejection(err))
			if tc.want {
				assert.False(t, providers.IsRetryable(err),
					"a capability rejection must not trigger same-model retries")
				assert.False(t, providers.IsUpstreamModelNotFound(err),
					"a capability rejection must not be confused with model-not-found")
			}
		})
	}

	assert.False(t, providers.IsUpstreamCapabilityRejection(nil))
	assert.False(t, providers.IsUpstreamCapabilityRejection(fmt.Errorf("transport blew up")))
}
