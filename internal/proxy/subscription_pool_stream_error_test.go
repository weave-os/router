package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// TestProxyMessages_ManagedPoolFailureFramesTheLiveStream: once the prelude is
// client-visible the handler can no longer render an envelope, so the pool
// failure has to terminate the stream with an error frame instead of leaving
// the client on a truncated stream.
func TestProxyMessages_ManagedPoolFailureFramesTheLiveStream(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}}}
	anthropic := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{
			Status: http.StatusTooManyRequests,
			Body:   []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"quota exhausted"}}`),
		}},
	}}
	svc := NewService(
		fixedRouter{decision: router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "claude-opus-4-8",
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"claude-opus-4-8"}},
		}},
		map[string]providers.Client{providers.ProviderAnthropic: anthropic},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderClaude),
		InstallationIDContextKey{}, "44444444-4444-4444-4444-444444444444")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(ctx, body, rec, req)

	require.ErrorIs(t, err, ErrSubscriptionPoolExhausted,
		"the pool sentinel must survive for the handler to classify and log")
	out := rec.Body.String()
	assert.Equal(t, 1, strings.Count(out, "event: error"),
		"the stream must terminate with exactly one error frame")
	assert.Contains(t, out, "All enrolled subscription accounts are currently unavailable.")
}

// TestProxyMessages_ManagedPoolFailureBeforeAnyBytesLeavesRenderingToHandler:
// pre-commit the classified envelope is the handler's to write, so the proxy
// must leave the response body untouched.
func TestProxyMessages_ManagedPoolFailureBeforeAnyBytesLeavesRenderingToHandler(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}}}
	anthropic := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{
			Status: http.StatusTooManyRequests,
			Body:   []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"quota exhausted"}}`),
		}},
	}}
	svc := NewService(
		fixedRouter{decision: router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "claude-opus-4-8",
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"claude-opus-4-8"}},
		}},
		map[string]providers.Client{providers.ProviderAnthropic: anthropic},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderClaude),
		InstallationIDContextKey{}, "66666666-6666-6666-6666-666666666666")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(ctx, body, rec, req)

	require.ErrorIs(t, err, ErrSubscriptionPoolExhausted)
	assert.Empty(t, rec.Body.String(),
		"a pre-commit pool failure is the handler's envelope to render")
}
