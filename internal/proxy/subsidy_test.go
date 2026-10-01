package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
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

// End-to-end: the key withUsageObserver records under must equal the key
// subsidyFactors reads, or the discount never materializes. Drives the real
// observer closure (as a provider would) with a resolved Codex credential and an
// upstream rate-limit response, then asserts subsidyFactors returns the discount.
func TestSubsidy_RecordReadKeyAgreement(t *testing.T) {
	s := (&Service{}).WithSubscriptionAwareRouting(
		usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now), 0.05, 2.0)

	const jwt = "eyJhbGciOi.codex.jwt"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+jwt)
	headers.Set("ChatGPT-Account-ID", "acct-1")

	ctx := context.Background()
	obsCtx := s.withUsageObserver(ctx, headers)

	// Simulate the resolved Codex credential + an upstream response at 10% used,
	// invoked the way a provider does after the upstream call.
	cred := &Credentials{APIKey: []byte(jwt), AccountID: []byte("acct-1"), Source: credSourceCodexSubscription, OAuth: true}
	callCtx := context.WithValue(obsCtx, CredentialsContextKey{}, cred)
	resp := http.Header{}
	resp.Set("x-codex-primary-used-percent", "10")
	resp.Set("x-codex-primary-window-minutes", "300")
	providers.ObserveUpstreamHeaders(callCtx, resp)

	// subsidyFactors must read back the SAME key and discount covered GPT models.
	factors := s.subsidyFactors(ctx, headers)
	require.NotNil(t, factors, "headroom was observed; factors must be non-nil")
	f, ok := factors["gpt-5.6-sol"]
	require.True(t, ok, "covered GPT model must be subsidized")
	assert.Less(t, f, 1.0, "10%% used → discounted below full price")
	assert.GreaterOrEqual(t, f, 0.05, "never below epsilon")
	assert.NotContains(t, factors, "gpt-5.4-nano",
		"infrastructure OpenAI models must not receive the caller-subscription discount")
}

func TestClaudeOverageStopsSubscriptionRouting(t *testing.T) {
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	service := (&Service{deploymentKeyedProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}).
		WithSubscriptionAwareRouting(observer, 0.05, 2.0)
	const token = "sk-ant-oat01-overage"
	headers := http.Header{"Authorization": []string{"Bearer " + token}}

	assert.Nil(t, service.subsidyFactors(context.Background(), headers),
		"an unobserved Claude subscription must not be assumed free")

	callCtx := service.withUsageObserver(context.Background(), headers)
	callCtx = context.WithValue(callCtx, CredentialsContextKey{}, &Credentials{
		APIKey: []byte(token), Source: credSourceSubscription, OAuth: true,
	})
	responseHeaders := http.Header{}
	responseHeaders.Set("anthropic-ratelimit-unified-representative-claim", "overage")
	responseHeaders.Set("anthropic-ratelimit-unified-overage-in-use", "true")
	responseHeaders.Set("anthropic-ratelimit-unified-overage-reset", "2026-10-01T00:00:00Z")
	providers.ObserveUpstreamHeaders(callCtx, responseHeaders)

	assert.Nil(t, service.subsidyFactors(context.Background(), headers),
		"billable overage must not discount Claude models")
	assert.True(t, service.claudeSubscriptionExhausted(context.Background(), headers),
		"billable overage must fall through to the deployment key")
}

func TestClaudeOverageDispatchesOnDeploymentKey(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{okBody: ingress.upstreamOK(false)}
	service := ingress.parityService(upstream)
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	service.WithSubscriptionAwareRouting(observer, 0.05, 2.0)
	observer.Record(observer.Key([]byte(ingress.token)), usage.Snapshot{OverageInUse: true})

	recorder, request, body := ingress.request(t, false)
	require.NoError(t, ingress.call(service, ingress.subCtx(), body, recorder, request))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Zero(t, upstream.subDispatches, "billable Claude OAuth must be suppressed before dispatch")
	assert.Equal(t, 1, upstream.paidDispatches, "the turn must use the Weave deployment key")
}

func TestSubsidyFactors_ClaudeColdStartIsNeutral(t *testing.T) {
	s := (&Service{}).WithSubscriptionAwareRouting(
		usage.NewObserver([]byte("salt"), time.Minute, time.Now), 0.05, 2.0)

	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-cold")
	assert.Nil(t, s.subsidyFactors(context.Background(), h),
		"unknown Claude billing state must not bias routing toward Anthropic")

	h.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
	h.Set("ChatGPT-Account-ID", "acct-1")
	factors := s.subsidyFactors(context.Background(), h)
	require.NotNil(t, factors, "Codex retains its observed-account bootstrap")
	assert.InDelta(t, 0.05, factors["gpt-5.6-sol"], 1e-9)
}

// Per-installation opt-out: when the org has disabled subscription-aware
// routing, a present subscription must produce NO subsidy factors so the scorer
// adds no Claude bonus and non-Claude models compete on merits. Mirrors the
// cold-start case but with the disable flag stashed on ctx by the auth
// middleware — the discount is otherwise non-nil there, so this asserts the
// flag is what suppresses it.
func TestSubsidyFactors_DisabledForInstallation(t *testing.T) {
	observer := usage.NewObserver([]byte("salt"), time.Minute, time.Now)
	s := (&Service{}).WithSubscriptionAwareRouting(observer, 0.05, 2.0)

	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-cold")
	observer.Record(observer.Key([]byte("sk-ant-oat01-cold")), usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.10, WindowMinutes: 300},
	})

	// Sanity: without the flag the same request DOES subsidize (guards against a
	// vacuous pass if the sub stopped being detected).
	require.NotNil(t, s.subsidyFactors(context.Background(), h),
		"baseline: a present sub subsidizes when routing is not disabled")

	ctx := context.WithValue(context.Background(), InstallationSubscriptionRoutingDisabledContextKey{}, true)
	assert.Nil(t, s.subsidyFactors(ctx, h),
		"subscription routing disabled → no subsidy bonus, route on merits")
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
