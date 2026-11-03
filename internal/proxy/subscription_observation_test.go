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

type weeklyUsageCapture struct {
	readings chan recordedWeeklyUsage
}

type recordedWeeklyUsage struct {
	accountID string
	window    usage.Window
}

func (capture *weeklyUsageCapture) RecordWeeklyUsage(_ context.Context, accountID string, window usage.Window) error {
	capture.readings <- recordedWeeklyUsage{accountID: accountID, window: window}
	return nil
}

func TestWeeklyUsagePersistsOnlyTheDispatchedAccountsReportedWeek(t *testing.T) {
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	for _, source := range []string{credSourceSubscription, credSourceCodexSubscription} {
		t.Run(source, func(t *testing.T) {
			capture := &weeklyUsageCapture{readings: make(chan recordedWeeklyUsage, 4)}
			service := (&Service{now: func() time.Time { return now }}).
				WithUsageObserver(usage.NewObserver([]byte("synthetic"), time.Minute, func() time.Time { return now })).
				WithWeeklyUsageRecorder(capture)
			ctx := service.withUsageObserver(context.Background(), http.Header{})
			ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
				APIKey: []byte("synthetic-token"), OAuth: true, Source: source, SubscriptionAccountID: "physical-account",
			})
			headers := http.Header{}
			if source == credSourceSubscription {
				headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.63")
				headers.Set("anthropic-ratelimit-unified-7d-reset", now.Add(48*time.Hour).Format(time.RFC3339))
			} else {
				headers.Set("x-codex-secondary-used-percent", "63")
				headers.Set("x-codex-secondary-reset-at", now.Add(48*time.Hour).Format(time.RFC3339))
			}
			providers.ObserveUpstreamHeaders(ctx, headers)
			select {
			case reading := <-capture.readings:
				require.Equal(t, "physical-account", reading.accountID)
				require.InDelta(t, 0.63, reading.window.UsedPercent, 0.0001)
				require.Equal(t, now, reading.window.ObservedAt)
				require.Equal(t, now.Add(48*time.Hour), reading.window.ResetAt)
			case <-time.After(time.Second):
				t.Fatal("weekly observation was not persisted")
			}
			providers.ObserveUpstreamHeaders(ctx, http.Header{})
			service.recordSubscriptionUsage(ctx, CredentialsFromContext(ctx), usage.Snapshot{Primary: usage.Window{WindowMinutes: 300, UsedPercent: 0.9}})
			service.recordSubscriptionUsage(ctx, &Credentials{APIKey: []byte("direct-token"), OAuth: true, Source: source}, usage.Snapshot{Secondary: usage.Window{WindowMinutes: 10080, UsedPercent: 0.1}})
			require.Empty(t, capture.readings, "missing weekly headers and unenrolled tokens must not overwrite saved usage")
		})
	}
}

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

// Paid extra usage means included quota is spent: OAuth is suppressed and the deployment key serves.
func TestClaudeOverageAuthorizesDeploymentKey(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{okBody: ingress.upstreamOK(false)}
	service := ingress.parityService(upstream)
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	service.WithUsageObserver(observer)
	observer.Record(observer.Key([]byte(ingress.token)), usage.Snapshot{OverageInUse: true, UnifiedResetAt: time.Now().Add(time.Hour)})

	recorder, request, body := ingress.request(t, false)
	require.NoError(t, ingress.call(service, ingress.subCtx(), body, recorder, request))
	assert.Zero(t, upstream.subDispatches, "billable Claude OAuth must be suppressed before dispatch")
	assert.Equal(t, 1, upstream.paidDispatches, "observed overage authorizes API inference")
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
