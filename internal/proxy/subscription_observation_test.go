package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/subscriptions/entitlement"
)

// The subsidy must recognize a subscription presented via the inbound
// Authorization bearer (Claude Code's sk-ant-oat…, Codex CLI's JWT+account-id on
// their native harnesses), not only via a router-keyed context
// headers (opencode). Otherwise the discount would be opencode-only.
func TestPresentSubscriptionTokens_InboundBearerHarnesses(t *testing.T) {
	t.Run("claude code: sk-ant-oat in Authorization", func(t *testing.T) {
		h := http.Header{}
		h.Set("Authorization", "Bearer sk-ant-oat01-claudecode-token")
		codex, anthro := presentSubscriptionTokens(context.Background(), h)
		assert.Equal(t, "sk-ant-oat01-claudecode-token", anthro)
		assert.Empty(t, codex, "an sk-ant token must not be misread as a Codex sub")
	})

	t.Run("codex cli: JWT + ChatGPT-Account-ID in Authorization", func(t *testing.T) {
		h := http.Header{}
		h.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
		h.Set("ChatGPT-Account-ID", "acct-abc-123")
		codex, anthro := presentSubscriptionTokens(context.Background(), h)
		assert.Equal(t, "eyJhbGciOi.codex.jwt", codex)
		assert.Empty(t, anthro, "a Codex JWT must not be misread as a Claude sub")
	})

	t.Run("codex jwt without account-id is not a usable codex sub", func(t *testing.T) {
		h := http.Header{}
		h.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
		codex, _ := presentSubscriptionTokens(context.Background(), h)
		assert.Empty(t, codex, "the Codex backend needs the account-id; no pairing = no sub")
	})

	t.Run("no subscription credentials: both empty", func(t *testing.T) {
		codex, anthro := presentSubscriptionTokens(context.Background(), http.Header{})
		assert.Empty(t, codex)
		assert.Empty(t, anthro)
	})
}

// Toggle off: presentSubscriptionTokens must report none so the usage-bypass and
// balance-gate paths agree the turn is prepaid, not free on the deployment key.
func TestPresentSubscriptionTokens_DisabledReportsNone(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-live-token")
	ctx := context.WithValue(context.Background(), InstallationSubscriptionRoutingDisabledContextKey{}, true)

	codex, anthro := presentSubscriptionTokens(ctx, h)
	assert.Empty(t, anthro, "toggle off must report no Claude sub so billing paths gate prepaid")
	assert.Empty(t, codex)

	assert.False(t, RequestPresentsCoveringSubscription(ctx, h, routePathMessages),
		"toggle off must not exempt the balance gate")

	// Sanity: the same request WITH the toggle on does present the sub.
	assert.True(t, RequestPresentsCoveringSubscription(context.Background(), h, routePathMessages))
}

// Billable subscription observations suppress OAuth before dispatch and use authorized API capacity.
func TestClaudeOverageDispatchesOnDeploymentKey(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{okBody: ingress.upstreamOK(false)}
	service := ingress.parityService(upstream)
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	service.WithUsageObserver(observer)
	observer.Record(observer.Key([]byte(ingress.token)), usage.Snapshot{OverageInUse: true})

	recorder, request, body := ingress.request(t, false)
	require.NoError(t, ingress.call(service, ingress.subCtx(), body, recorder, request))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Zero(t, upstream.subDispatches, "billable Claude OAuth must be suppressed before dispatch")
	assert.Equal(t, 1, upstream.paidDispatches, "the turn must use the Weave deployment key")
}

func TestPresentSubscriptionTokens_MaxProductScopeReportsNone(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-live-token")
	ctx := entitlement.WithProductScope(context.Background(), entitlement.PlanMax)

	codex, anthro := presentSubscriptionTokens(ctx, h)
	assert.Empty(t, anthro)
	assert.Empty(t, codex)
	assert.False(t, RequestPresentsCoveringSubscription(ctx, h, routePathMessages))

	enrolled := context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
	assert.False(t, RequestPresentsCoveringSubscription(enrolled, http.Header{}, routePathMessages))
}
