package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

func TestSubscriptionFundingPaidDispatchRequiresQuotaProof(t *testing.T) {
	now := time.Now()
	resetAt := now.Add(time.Hour)
	for _, tc := range []struct {
		name     string
		accounts []*auth.SubscriptionAccount
		allowed  bool
	}{
		{name: "no compatible pool", accounts: []*auth.SubscriptionAccount{{ID: "codex", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}}, allowed: true},
		{name: "unknown quota", accounts: []*auth.SubscriptionAccount{{ID: "claude", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateUnknown}}},
		{name: "active quota", accounts: []*auth.SubscriptionAccount{{ID: "claude", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}}},
		{name: "cooldown is not exhaustion", accounts: []*auth.SubscriptionAccount{{ID: "claude", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &resetAt}}},
		{name: "reconnect-required seat cannot block API", accounts: []*auth.SubscriptionAccount{{ID: "claude", Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateReconnectRequired}}, allowed: true},
		{name: "disabled seat cannot block API", accounts: []*auth.SubscriptionAccount{{ID: "claude", Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateDisabled}}, allowed: true},
		{name: "dead seat beside exhausted seat", accounts: []*auth.SubscriptionAccount{
			{ID: "spent", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &resetAt},
			{ID: "dead", Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateReconnectRequired},
		}, allowed: true},
		{name: "dead seat beside active seat", accounts: []*auth.SubscriptionAccount{
			{ID: "usable", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
			{ID: "dead", Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateDisabled},
		}},
		{name: "all quota exhausted", accounts: []*auth.SubscriptionAccount{{ID: "claude", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &resetAt}}, allowed: true},
		{name: "reset restores included lane", accounts: []*auth.SubscriptionAccount{{ID: "claude", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &now}}},
		{name: "one exhausted one active", accounts: []*auth.SubscriptionAccount{
			{ID: "spent", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &resetAt},
			{ID: "usable", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
		}},
	} {
		for _, source := range []string{"", requestcontext.SourceBYOK, requestcontext.SourceClient} {
			t.Run(tc.name+"/"+source, func(t *testing.T) {
				upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("paid answer")}}}
				svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream})
				ctx := WithManagedSubscriptionCandidates(context.Background(), tc.accounts)
				ctx = svc.withSubscriptionFundingAuthorization(ctx, nil)
				// Exhausted-state routing and OAuth suppression must not erase admission facts.
				ctx = context.WithValue(withSuppressedClaudeSubscription(ctx), subscriptionAPIOnlyKey{}, true)
				if source != "" {
					ctx = requestcontext.WithCredentials(ctx, &Credentials{Source: source, APIKey: []byte("synthetic-api-key")})
				}
				rec := httptest.NewRecorder()
				buf := newPreludeBuffer(rec)
				in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
				in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
				_, err := svc.dispatchWithFallback(ctx, in)
				if tc.allowed {
					require.NoError(t, err)
					require.Equal(t, 1, upstream.calls)
					require.Equal(t, "paid answer", rec.Body.String())
				} else {
					require.ErrorIs(t, err, dispatch.ErrAuthorizationDenied)
					require.Zero(t, upstream.calls)
				}
			})
		}
	}
}

func TestSubscriptionFundingFinalOAuthLeaseIsAllowed(t *testing.T) {
	upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("included answer")}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "seat", AccessToken: "oauth-token"}}})
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
	_, err := svc.dispatchWithFallback(ctx, in)
	require.NoError(t, err)
	require.Equal(t, "included answer", rec.Body.String())
}

func TestSubscriptionFundingUnknownEnrollmentAndGatewayBinding(t *testing.T) {
	svc := newServiceWithProviders(t, nil)
	unknownCtx := context.WithValue(context.Background(), ManagedSubscriptionEnrollmentUnavailableContextKey{}, true)
	require.ErrorIs(t, svc.subscriptionAPIAuthorization(unknownCtx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String()), ErrSubscriptionPoolUnavailable)
	// Gemini has no compatible subscription pool; unrelated uncertainty cannot block it.
	_, known := catalog.ByID(catalog.ModelIDGemini31ProPreview.String())
	require.True(t, known)
	require.NoError(t, svc.subscriptionAPIAuthorization(unknownCtx, providers.ProviderGoogle, catalog.ModelIDGemini31ProPreview.String()))
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	require.ErrorIs(t, svc.subscriptionAPIAuthorization(ctx, providers.ProviderAnthropicGateway, catalog.ModelIDClaudeOpus48.String()), ErrSubscriptionPoolUnavailable,
		"a gateway binding must not bypass catalog-compatible pool authorization")
}

func TestSubscriptionRecoveryDoesNotExpandAdmissionSnapshot(t *testing.T) {
	upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("unadmitted answer")}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "new-seat", AccessToken: "new-token"}}})
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "initial-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
	_, err := svc.dispatchWithFallback(ctx, in)
	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable)
	require.Zero(t, upstream.calls)
}

func TestSubscriptionLeaseOutsideSnapshotFallsThroughToAdmittedSeat(t *testing.T) {
	upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("admitted answer")}}}
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "new-seat", AccessToken: "new-token"}, {AccountID: "initial-seat", AccessToken: "initial-token"}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).WithManagedSubscriptions(leaser)
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "initial-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
	_, err := svc.dispatchWithFallback(ctx, in)
	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, "admitted answer", rec.Body.String())
}

func TestSubscriptionLeaseOutsideSnapshotBlocksPaidRecovery(t *testing.T) {
	upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("unexpected paid answer")}}}
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "new-seat", AccessToken: "new-token"}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	resetAt := time.Now().Add(time.Hour)
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "spent-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &resetAt}})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
	_, err := svc.dispatchWithFallback(ctx, in)
	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable)
	require.NotErrorIs(t, err, ErrSubscriptionPoolExhausted)
	require.Zero(t, upstream.calls, "an unproven unadmitted seat must not authorize paid recovery")
}

func TestSubscriptionFundingLiveQuotaProofAllowsAPIWithoutObserver(t *testing.T) {
	upstream := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"usage_limit_reached"}}`)}},
		{writeBytes: []byte("paid rollover")},
	}}
	leaser := &healthSubscriptionLeaser{scriptedSubscriptionLeaser: &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "seat", AccessToken: "oauth-token"}}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: upstream}).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel}
	_, err := svc.dispatchWithFallback(ctx, in)
	require.NoError(t, err)
	require.Equal(t, 2, upstream.calls)
	require.Equal(t, []string{"seat"}, leaser.exhaustedIDs)
	require.Equal(t, "paid rollover", rec.Body.String())
}

// A buffered provider 5xx on the plan is the one operational failure that
// does buy paid rescue: the plan could not serve, and the turn moves on.
func TestSubscriptionFundingProviderFailureAuthorizesPaidRescue(t *testing.T) {
	failure := &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(`{"error":{"type":"service_unavailable_error","code":"server_is_overloaded"}}`)}
	upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{err: failure}, {writeBytes: []byte("paid rescue answer")}}}
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "seat", AccessToken: "oauth-token"}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
	_, err := svc.dispatchWithFallback(ctx, in)
	require.NoError(t, err)
	require.Equal(t, 2, upstream.calls, "the plan attempt fails, the paid attempt serves")
	require.Contains(t, rec.Body.String(), "paid rescue answer")
}

func TestSubscriptionFundingOperationalFailuresNeverAuthorizeAPI(t *testing.T) {
	for _, failure := range []error{
		&providers.UpstreamErrorResponse{Status: http.StatusUnauthorized},
		&providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"rate_limit_error"}}`)},
		&providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(`{"error":{"type":"invalid_request_error","message":"Third-party apps now draw from your extra usage, not your plan limits"}}`)},
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{err: failure}, {writeBytes: []byte("unexpected paid answer")}}}
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "seat", AccessToken: "oauth-token"}}}
			svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
			ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
			rec := httptest.NewRecorder()
			buf := newPreludeBuffer(rec)
			in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
			in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
			_, err := svc.dispatchWithFallback(ctx, in)
			require.Error(t, err)
			require.ErrorIs(t, err, failure, "rotation must retain the actual upstream failure")
			require.Equal(t, 1, upstream.calls)
			require.NotContains(t, rec.Body.String(), "unexpected paid answer")
		})
	}
}

func TestSubscriptionFundingBufferedAuxiliaryCannotClearGuard(t *testing.T) {
	upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("unexpected summary")}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream})
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	ctx = clearCredentials(svc.withSubscriptionFundingAuthorization(ctx, nil))
	summarizer := newTestSummarizer(t, upstream, policy.HandoverSummaryDefaultModel, time.Second)
	plan, err := summarizer.plans.Resolve(policy.ResolutionRequest{Purpose: inference.PurposeHandoverSummary, RouterRequest: router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}})
	require.NoError(t, err)
	transport := dispatch.Buffered{Prepare: func(context.Context, dispatch.Attempt) (providers.PreparedRequest, *http.Request, error) {
		return providers.PreparedRequest{}, httptest.NewRequest(http.MethodPost, "/v1/messages", nil), nil
	}}.Transport()
	_, err = summarizer.executor.Run(ctx, inference.InvocationRequest{Purpose: plan.Purpose()}, plan, transport)
	require.ErrorIs(t, err, dispatch.ErrAuthorizationDenied)
	require.Zero(t, upstream.calls)
}

func TestSubscriptionFundingObservedQuotaAndAdmissionSnapshotAreDetached(t *testing.T) {
	resetAt := time.Now().Add(time.Hour)
	account := &auth.SubscriptionAccount{ID: "seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive, CooldownUntil: &resetAt}
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{account})
	account.State = auth.SubscriptionAccountStateExhausted
	svc := newServiceWithProviders(t, nil)
	require.ErrorIs(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderClaude, "", "", clientAppRefusalFunding{}), ErrSubscriptionPoolUnavailable)
	observer := usage.NewObserver([]byte("synthetic-salt"), time.Minute, time.Now)
	observer.Record(observer.Key([]byte("subscription-account:seat")), usage.Snapshot{Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300, ResetAt: resetAt}})
	svc.WithUsageObserver(observer)
	require.NoError(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderClaude, "", "", clientAppRefusalFunding{}))
}

func TestSubscriptionAdmissionBudgetChargesLeasingNotInference(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := newServiceWithProviders(t, nil)
		ctx := svc.withSubscriptionFundingAuthorization(context.Background(), nil)
		ctx, budget := withSubscriptionAdmissionBudget(ctx)
		leaseCtx, finish := budget.leaseContext(ctx, time.Now)
		time.Sleep(4 * time.Second)
		require.NoError(t, leaseCtx.Err())
		finish()
		time.Sleep(30 * time.Second)
		require.Equal(t, 6*time.Second, budget.remaining)
		reusedCtx, reused := withSubscriptionAdmissionBudget(svc.withSubscriptionFundingAuthorization(clearCredentials(ctx), nil))
		require.Same(t, budget, reused)
		leaseCtx, finish = reused.leaseContext(reusedCtx, time.Now)
		time.Sleep(7 * time.Second)
		require.ErrorIs(t, leaseCtx.Err(), context.DeadlineExceeded)
		finish()
		require.Zero(t, budget.remaining)
	})
}

func TestSubscriptionFundingExpiredShortWindowCannotAuthorizeAPI(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, refreshWeekly := range []bool{false, true} {
			t.Run(fmt.Sprintf("direct=%t/refresh_weekly=%t", direct, refreshWeekly), func(t *testing.T) {
				observedAt := time.Unix(1_800_000_000, 0)
				now := observedAt
				observer := usage.NewObserver([]byte("synthetic-salt"), time.Minute, func() time.Time { return now })
				svc := newServiceWithProviders(t, nil).WithUsageObserver(observer)
				svc.now = func() time.Time { return now }
				ctx := context.Background()
				credential, directToken := "subscription-account:seat", ""
				if direct {
					credential, directToken = "synthetic-direct-token", "synthetic-direct-token"
				} else {
					ctx = WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: "seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}})
				}
				key := observer.Key([]byte(credential))
				observer.Record(key, usage.Snapshot{Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300}, Secondary: usage.Window{UsedPercent: 0.6, WindowMinutes: 10080}})
				if refreshWeekly {
					now = observedAt.Add(2 * time.Hour)
					observer.Record(key, usage.Snapshot{Secondary: usage.Window{UsedPercent: 0.6, WindowMinutes: 10080}})
				}
				now = observedAt.Add(4 * time.Hour)
				require.NoError(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, directToken, "", clientAppRefusalFunding{}))
				now = observedAt.Add(6 * time.Hour)
				_, cached := observer.Snapshot(key)
				require.True(t, cached, "the weekly reading intentionally keeps this snapshot cached")
				require.ErrorIs(t, svc.authorizeSubscriptionAPI(ctx, subscriptions.ProviderCodex, directToken, "", clientAppRefusalFunding{}), ErrSubscriptionPoolUnavailable,
					"a stale short-window cap is not current quota proof")
			})
		}
	}
}

func TestSubscriptionFundingPaidOverageIsQuotaProof(t *testing.T) {
	upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("paid answer")}}}
	now := time.Unix(1_800_000_000, 0)
	observer := usage.NewObserver([]byte("synthetic-salt"), 24*time.Hour, func() time.Time { return now })
	observer.Record(observer.Key([]byte("subscription-account:seat")), usage.Snapshot{OverageInUse: true, UnifiedResetAt: now.Add(time.Hour)})
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).
		WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "seat", AccessToken: "oauth-token"}}}).
		WithUsageObserver(observer).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	svc.now = func() time.Time { return now }
	ctx := WithManagedSubscriptionCandidates(context.Background(), []*auth.SubscriptionAccount{{ID: "seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
	_, err := svc.dispatchWithFallback(ctx, in)
	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, "paid answer", rec.Body.String())
}

func TestSubscriptionFundingDirectTokenOverageIsQuotaProof(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	observer := usage.NewObserver([]byte("synthetic-salt"), 24*time.Hour, func() time.Time { return now })
	observer.Record(observer.Key([]byte("synthetic-direct-token")), usage.Snapshot{OverageInUse: true, UnifiedResetAt: now.Add(time.Hour)})
	svc := newServiceWithProviders(t, nil).WithUsageObserver(observer)
	svc.now = func() time.Time { return now }
	require.NoError(t, svc.authorizeSubscriptionAPI(context.Background(), subscriptions.ProviderClaude, "", "synthetic-direct-token", clientAppRefusalFunding{}))
	now = now.Add(2 * time.Hour)
	require.ErrorIs(t, svc.authorizeSubscriptionAPI(context.Background(), subscriptions.ProviderClaude, "", "synthetic-direct-token", clientAppRefusalFunding{}), ErrSubscriptionPoolUnavailable,
		"overage past the plan reset is not current quota proof")
	observer.Record(observer.Key([]byte("synthetic-undated-token")), usage.Snapshot{OverageInUse: true})
	require.ErrorIs(t, svc.authorizeSubscriptionAPI(context.Background(), subscriptions.ProviderClaude, "", "synthetic-undated-token", clientAppRefusalFunding{}), ErrSubscriptionPoolUnavailable,
		"overage without a reported plan reset cannot authorize paid inference indefinitely")
}
