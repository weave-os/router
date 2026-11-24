package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

// Capture must work when s.credentials.UsageObserver is nil — the Phase 0 dogfooding
// scenario before subsidy-aware routing is enabled.
func TestWithUsageObserver_CapturesRawHeadersEvenWithoutSubsidyObserver(t *testing.T) {
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()} // usageObserver is nil: subsidy feature not configured

	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-ant-oat01-claudecode")

	ctx := s.withUsageObserver(context.Background(), headers)
	callCtx := context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("sk-ant-oat01-claudecode"), Source: requestcontext.SourceSubscription, OAuth: true,
	})

	resp := http.Header{}
	resp.Set("anthropic-ratelimit-unified-status", "allowed")
	resp.Set("anthropic-ratelimit-unified-overage-disabled-reason", "org_level_disabled")
	providers.ObserveUpstreamHeaders(callCtx, resp)

	got := credentials.UnifiedLimitHeadersFrom(ctx)
	require.NotNil(t, got, "raw headers must be captured independent of the subsidy observer")
	assert.Equal(t, "allowed", got["anthropic-ratelimit-unified-status"])
	assert.Equal(t, "org_level_disabled", got["anthropic-ratelimit-unified-overage-disabled-reason"])
}

// No-op contract: nothing captured when usageObserver is nil AND no subscription
// token is present — Phase 0 must not capture on every request.
func TestWithUsageObserver_NoopWhenNothingToObserve(t *testing.T) {
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}
	ctx := context.Background()
	got := s.withUsageObserver(ctx, http.Header{}) // no Authorization at all
	assert.Nil(t, credentials.UnifiedLimitHeadersFrom(got), "no subscription token present -> capture never installed")
}

func TestWithUsageObserver_CapturesCodexHeadersForDirectCodexLogin(t *testing.T) {
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
	headers.Set("ChatGPT-Account-ID", "acct-abc-123")

	ctx := s.withUsageObserver(context.Background(), headers)
	callCtx := context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("eyJhbGciOi.codex.jwt"), Source: requestcontext.SourceCodexSubscription, OAuth: true,
	})

	resp := http.Header{}
	resp.Set("x-codex-primary-used-percent", "55")
	providers.ObserveUpstreamHeaders(callCtx, resp)

	assert.Equal(t, "55", credentials.CodexLimitHeadersFrom(ctx)["x-codex-primary-used-percent"])
}
