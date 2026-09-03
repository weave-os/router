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

// TestIsUpstreamCapabilityRejection checks phrase matching and asserts that
// capability rejections are neither retryable nor model-not-found.
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

// TestIsUpstreamSchemaRejection pins the phrase set for the Fireworks
// grammar-compiler 400 ("Conflict in schema definitions") and asserts the class
// is NOT treated as same-binding-retryable, model-not-found, or a capability
// rejection — it is a cross-binding rescue signal only.
func TestIsUpstreamSchemaRejection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "fireworks schema-definition conflict",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"Conflict in schema definitions for key ‘description’. Previous: (pattern: ^[^\\n\\r]*$), New: (pattern: ^[\\s\\S]{0,300}$)"}}`,
			want:   true,
		},
		{
			name:   "grammar compile failure",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"failed to compile grammar for tool 'Task'"}}`,
			want:   true,
		},
		{
			name:   "case insensitive",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"Invalid Tool Schema: unexpected key"}}`,
			want:   true,
		},
		{
			name:   "ordinary validation 400 is not a schema rejection",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"messages: at least one message is required"}}`,
			want:   false,
		},
		{
			name:   "capability rejection is a different class",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"not a multimodal model"}}`,
			want:   false,
		},
		{
			name:   "500 is not a schema rejection",
			status: http.StatusInternalServerError,
			body:   `{"error":{"message":"Conflict in schema definitions"}}`,
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &providers.UpstreamErrorResponse{Status: tc.status, Body: []byte(tc.body)}
			assert.Equal(t, tc.want, providers.IsUpstreamSchemaRejection(err))
			if tc.want {
				assert.False(t, providers.IsRetryable(err),
					"a schema rejection must not trigger same-binding retries (identical re-POST 400s)")
				assert.False(t, providers.IsUpstreamModelNotFound(err))
				assert.False(t, providers.IsUpstreamCapabilityRejection(err),
					"schema rejection is cross-binding retryable; capability rejection is not")
			}
		})
	}
	assert.False(t, providers.IsUpstreamSchemaRejection(nil))
	assert.False(t, providers.IsUpstreamSchemaRejection(fmt.Errorf("transport blew up")))
}

// TestIsUpstreamOutputConfigFormatRejection pins the gateway 400 that names
// the structured-output knob as an unknown field; a schema-contents complaint
// naming the same field must not match — it would silently unstructure a turn
// the caller could have fixed.
func TestIsUpstreamOutputConfigFormatRejection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "gateway rejects the structured-output knob",
			status: http.StatusBadRequest,
			body:   `{"message":"output_config.format: Extra inputs are not permitted"}`,
			want:   true,
		},
		{
			name:   "pydantic loc list phrasing",
			status: http.StatusBadRequest,
			body:   `{"detail":[{"loc":["body","output_config","format"],"msg":"extra fields not permitted"}]}`,
			want:   true,
		},
		{
			name:   "upstream dislikes the schema but serves the knob",
			status: http.StatusBadRequest,
			body:   `{"message":"output_config.format.schema: For 'object' type, 'additionalProperties' must be explicitly set to false"}`,
			want:   false,
		},
		{
			name:   "cortex rejects a missing schema member, not the knob",
			status: http.StatusBadRequest,
			body:   `{"message":"missing field ` + "`schema`" + ` at line 1 column 89"}`,
			want:   false,
		},
		{
			name:   "a different unknown output_config member is not this class",
			status: http.StatusBadRequest,
			body:   `{"message":"output_config.effort: Extra inputs are not permitted"}`,
			want:   false,
		},
		{
			name:   "unrelated validation 400",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"messages: at least one message is required"}}`,
			want:   false,
		},
		{
			name:   "500 mentioning the field is not a rejection",
			status: http.StatusInternalServerError,
			body:   `{"message":"output_config.format: Extra inputs are not permitted"}`,
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &providers.UpstreamErrorResponse{Status: tc.status, Body: []byte(tc.body)}
			assert.Equal(t, tc.want, providers.IsUpstreamOutputConfigFormatRejection(err))
			if tc.want {
				assert.False(t, providers.IsRetryable(err),
					"an identical re-POST 400s; only the knob-stripped re-emit may retry")
			}
		})
	}
	assert.False(t, providers.IsUpstreamOutputConfigFormatRejection(nil))
	assert.False(t, providers.IsUpstreamOutputConfigFormatRejection(fmt.Errorf("transport blew up")))
}

// TestIsAnthropicFastModeQuotaRejection pins the 429 Anthropic returns for an
// org without fast-mode allocation; an ordinary rate limit must not match, or
// the standard-speed retry would mask genuine throttling.
func TestIsAnthropicFastModeQuotaRejection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "no fast-mode allocation",
			status: http.StatusTooManyRequests,
			body:   `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your organization's rate limit of 0 fast mode input tokens per minute."}}`,
			want:   true,
		},
		{
			name:   "ordinary per-minute rate limit",
			status: http.StatusTooManyRequests,
			body:   `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your organization's rate limit of 50 requests per minute."}}`,
			want:   false,
		},
		{
			name:   "400 mentioning fast mode is not a quota rejection",
			status: http.StatusBadRequest,
			body:   `{"type":"error","error":{"type":"invalid_request_error","message":"fast mode is not available for this model"}}`,
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &providers.UpstreamErrorResponse{Status: tc.status, Body: []byte(tc.body)}
			assert.Equal(t, tc.want, providers.IsAnthropicFastModeQuotaRejection(err))
		})
	}
	assert.False(t, providers.IsAnthropicFastModeQuotaRejection(nil))
	assert.False(t, providers.IsAnthropicFastModeQuotaRejection(&providers.UpstreamStatusError{Status: http.StatusTooManyRequests}),
		"bytes already flushed to the client cannot be retried")
}

// TestIsUpstreamPromptCacheKeyRejection pins the gateway 400 that names prompt_cache_key
// as an unknown field; a 400 merely mentioning it for another reason must not match.
func TestIsUpstreamPromptCacheKeyRejection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "gateway rejects the affinity hint",
			status: http.StatusBadRequest,
			body:   `{"message":"prompt_cache_key: Extra inputs are not permitted"}`,
			want:   true,
		},
		{
			name:   "pydantic loc list phrasing",
			status: http.StatusBadRequest,
			body:   `{"detail":[{"loc":["body","prompt_cache_key"],"msg":"extra fields not permitted"}]}`,
			want:   true,
		},
		{
			name:   "serde unknown field phrasing",
			status: http.StatusBadRequest,
			body:   `{"message":"unknown field ` + "`prompt_cache_key`" + ` at line 1 column 42"}`,
			want:   true,
		},
		{
			name:   "400 disliking the key's contents is not this class",
			status: http.StatusBadRequest,
			body:   `{"message":"prompt_cache_key: string too long"}`,
			want:   false,
		},
		{
			name:   "unrelated unknown field is not this class",
			status: http.StatusBadRequest,
			body:   `{"message":"reasoning_effort: Extra inputs are not permitted"}`,
			want:   false,
		},
		{
			name:   "500 mentioning the field is not a rejection",
			status: http.StatusInternalServerError,
			body:   `{"message":"prompt_cache_key: unknown field"}`,
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &providers.UpstreamErrorResponse{Status: tc.status, Body: []byte(tc.body)}
			assert.Equal(t, tc.want, providers.IsUpstreamPromptCacheKeyRejection(err))
		})
	}
	assert.False(t, providers.IsUpstreamPromptCacheKeyRejection(nil))
	assert.False(t, providers.IsUpstreamPromptCacheKeyRejection(fmt.Errorf("transport blew up")))
}

// TestUpstreamErrorBodyMessage pins the buffered-body extraction used by the
// ProxyMessages complete log: nested error.message wins, top-level message is
// the fallback, and a non-JSON body is returned truncated rather than dropped.
func TestUpstreamErrorBodyMessage(t *testing.T) {
	nested := &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"message":"Conflict in schema definitions for key ‘description’","type":"invalid_request_error"}}`)}
	assert.Equal(t, "Conflict in schema definitions for key ‘description’", providers.UpstreamErrorBodyMessage(nested))

	toplevel := &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"message":"bad request"}`)}
	assert.Equal(t, "bad request", providers.UpstreamErrorBodyMessage(toplevel))

	raw := &providers.UpstreamErrorResponse{Status: 502, Body: []byte("Bad Gateway from LB")}
	assert.Equal(t, "Bad Gateway from LB", providers.UpstreamErrorBodyMessage(raw))

	assert.Equal(t, "", providers.UpstreamErrorBodyMessage(nil))
	assert.Equal(t, "", providers.UpstreamErrorBodyMessage(&providers.UpstreamStatusError{Status: 400}))
	assert.Equal(t, "", providers.UpstreamErrorBodyMessage(&providers.UpstreamErrorResponse{Status: 400}))

	big := &providers.UpstreamErrorResponse{Status: 400, Body: []byte(strings.Repeat("x", 5000))}
	assert.LessOrEqual(t, len(providers.UpstreamErrorBodyMessage(big)), 1024, "body is capped")
}

// TestIsUpstreamResponsesUnsupported pins the signals that mean "this upstream
// has no usable Responses API", so the caller re-emits onto chat/completions
// instead of failing the turn.
func TestIsUpstreamResponsesUnsupported(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "404 means the path is not mounted",
			status: http.StatusNotFound,
			body:   `{"message":"unknown path"}`,
			want:   true,
		},
		{
			name:   "cortex reports the api is off for the account",
			status: http.StatusBadRequest,
			body:   `{"message":"Cortex OpenAI Responses REST API not enabled","request_id":"x"}`,
			want:   true,
		},
		{
			name:   "403 entitlement prose still means no responses surface",
			status: http.StatusForbidden,
			body:   `{"message":"Responses API not enabled for this account"}`,
			want:   true,
		},
		{
			name:   "a real request rejection stays the caller's error",
			status: http.StatusBadRequest,
			body:   `{"message":"invalid request parameters: Function tools with reasoning_effort are not supported"}`,
			want:   false,
		},
		{
			name:   "an outage is not a capability verdict",
			status: http.StatusInternalServerError,
			body:   `{"message":"Responses API not enabled"}`,
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &providers.UpstreamErrorResponse{Status: tc.status, Body: []byte(tc.body)}
			assert.Equal(t, tc.want, providers.IsUpstreamResponsesUnsupported(err))
		})
	}

	assert.False(t, providers.IsUpstreamResponsesUnsupported(nil))
	assert.False(t, providers.IsUpstreamResponsesUnsupported(fmt.Errorf("transport blew up")))
}
