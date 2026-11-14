package proxy

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/subscriptions"
)

func enterpriseOnlyContext(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, claudeSubscriptionPlanKey{}, providers.SubscriptionPlanEnterprise)
	return flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyClaudeEnterpriseOnly: true}})
}

func withClaudeCreditFallback(ctx context.Context) context.Context {
	overrides, _ := flags.OverridesFromContext(ctx)
	overrides.Bools = maps.Clone(overrides.Bools)
	if overrides.Bools == nil {
		overrides.Bools = make(map[flags.Key]bool)
	}
	overrides.Bools[flags.KeyClaudeEnterpriseOnly] = false
	return flags.WithOverrides(ctx, overrides)
}

func TestClaudeOAuthAndOrgFlagDoNotEstablishEnterpriseIdentity(t *testing.T) {
	ctx := context.WithValue(routerKeyedCtx(), AnthropicSubscriptionContextKey{}, parityAnthropicToken)
	ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyClaudeEnterpriseOnly: true}})
	require.False(t, claudeEnterpriseOnly(ctx), "OAuth and an org-wide billing flag do not identify a provider subscription as Enterprise")
}

func TestClaudeEnterpriseOnlyRequiresVerifiedEnterprisePlan(t *testing.T) {
	ctx := context.WithValue(routerKeyedCtx(), AnthropicSubscriptionContextKey{}, parityAnthropicToken)
	require.False(t, claudeEnterpriseOnly(ctx))
	managed := context.WithValue(routerKeyedCtx(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
	require.False(t, claudeEnterpriseOnly(managed))
	require.True(t, claudeEnterpriseOnly(enterpriseOnlyContext(managed)))
	require.False(t, claudeEnterpriseOnly(routerKeyedCtx()), "API-only accounts have no Claude subscription to fund requests")
	optedOut := flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyClaudeEnterpriseOnly: false}})
	require.False(t, claudeEnterpriseOnly(optedOut))
	byok := parityAnthropicIngress().byokCtx(ctx)
	require.False(t, claudeEnterpriseOnly(byok), "explicit native BYOK retains precedence over the inherited default")
	require.False(t, claudeEnterpriseOnly(enterpriseOnlyContext(byok)), "explicit native BYOK retains precedence over a saved opt-in")

	service := newServiceWithProviders(t, nil)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+parityAnthropicToken)
	headerOnly := service.withSubscriptionFundingAuthorization(routerKeyedCtx(), headers)
	require.False(t, claudeEnterpriseOnly(headerOnly), "native OAuth is not a subscription-type attestation")
}

func TestClaudeEnterpriseOnlyClassifiersKeepOverage(t *testing.T) {
	ctx := enterpriseOnlyContext(context.WithValue(routerKeyedCtx(), AnthropicSubscriptionContextKey{}, classifierTestSubToken))
	service := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: &parityUpstream{}}).WithUsageObserver(observerWithSnapshot(classifierTestSubToken, usage.Snapshot{OverageInUse: true}))
	_, engaged := service.classifierPassthroughEngaged(ctx, http.Header{}, router.Request{RequestedModel: catalog.ModelIDClaudeSonnet46.String()}, turntype.Classifier)
	require.True(t, engaged)
}

func TestClaudeEnterpriseOnlyKeepsQuotaAndOverageOnOAuth(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, snapshot := range []usage.Snapshot{
			{Primary: usage.Window{UsedPercent: 0.5, WindowMinutes: 300}},
			{Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300}},
			{OverageInUse: true, Secondary: usage.Window{UsedPercent: 1, WindowMinutes: 10080}},
			{OverageInUse: true},
		} {
			t.Run(boolLit(stream), func(t *testing.T) {
				ingress := parityAnthropicIngress()
				upstream := &parityUpstream{okBody: ingress.upstreamOK(stream)}
				service := ingress.parityService(upstream)
				observer := usage.NewObserver([]byte("enterprise-test-salt"), time.Hour, time.Now)
				snapshot.UnifiedResetAt = time.Now().Add(time.Hour)
				observer.Record(observer.Key([]byte(ingress.token)), snapshot)
				service.WithUsageObserver(observer)
				recorder, request, body := ingress.request(t, stream)
				require.NoError(t, ingress.call(service, enterpriseOnlyContext(ingress.subCtx()), body, recorder, request))
				require.Equal(t, 1, upstream.subDispatches)
				require.Zero(t, upstream.paidDispatches)
			})
		}
	}
}

func TestClaudeEnterpriseOnlyLeasesOverageAccount(t *testing.T) {
	leaser := &healthSubscriptionLeaser{scriptedSubscriptionLeaser: &scriptedSubscriptionLeaser{
		leases: []subscriptions.Lease{{AccountID: "enterprise-seat", AccessToken: "enterprise-token"}},
	}}
	svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser).
		WithUsageObserver(observerWithSnapshot("enterprise-token", usage.Snapshot{OverageInUse: true}))
	_, lease, managed, err := svc.leaseManagedSubscription(enterpriseOnlyContext(managedSubscriptionTestContext()), providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())
	require.NoError(t, err)
	require.True(t, managed)
	require.Equal(t, "enterprise-seat", lease.AccountID)
	require.Empty(t, leaser.exhaustedIDs)
	lease.Release()
}

func TestClaudeEnterpriseOnlyPersistsActualQuotaRefusalAsCooldown(t *testing.T) {
	leaser := &healthSubscriptionLeaser{scriptedSubscriptionLeaser: &scriptedSubscriptionLeaser{}}
	service := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser)
	ctx := enterpriseOnlyContext(managedSubscriptionTestContext())
	err := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Headers: http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"1"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       []string{"4102444800"},
	}}
	rotated := service.recordManagedSubscriptionFailure(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String(), subscriptions.Lease{AccountID: "enterprise-seat"}, err)
	require.True(t, rotated)
	require.Equal(t, []string{"enterprise-seat"}, leaser.cooldownIDs)
	require.Empty(t, leaser.exhaustedIDs)
}

func TestVerifiedClaudeEnterpriseMovesFromIncludedUsageToOverage(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{okBody: ingress.upstreamOK(false), subHeaders: http.Header{
		"Anthropic-Ratelimit-Unified-Representative-Claim": []string{string(usage.AnthropicClaimOverage)},
		"Anthropic-Ratelimit-Unified-Overage-In-Use":       []string{"true"},
	}}
	service := ingress.quotaService(upstream)
	service.usageObserver.Record(service.usageObserver.Key([]byte(ingress.token)), usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.5, WindowMinutes: 300},
	})
	ctx := enterpriseOnlyContext(context.WithValue(routerKeyedCtx(), AnthropicSubscriptionContextKey{}, ingress.token))
	for range 2 {
		recorder, request, body := ingress.request(t, false)
		require.NoError(t, ingress.call(service, ctx, body, recorder, request))
		snapshot, observed := service.usageObserver.Snapshot(service.usageObserver.Key([]byte(ingress.token)))
		require.True(t, observed)
		require.True(t, snapshot.OverageInUse)
		require.InDelta(t, 0.5, snapshot.Primary.UsedPercent, 1e-9)
	}
	require.Equal(t, 2, upstream.subDispatches)
	require.Zero(t, upstream.paidDispatches)
}

func TestClaudeEnterpriseOnlyDefaultKeepsUnlinkedAPIRequestsFunded(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{okBody: ingress.upstreamOK(false)}
	service := ingress.parityService(upstream)
	recorder, request, body := ingress.request(t, false)
	require.NoError(t, ingress.call(service, routerKeyedCtx(), body, recorder, request))
	require.Equal(t, 1, upstream.paidDispatches)
	require.Zero(t, upstream.subDispatches)
}

func TestClaudeEnterpriseOnlyManagedFailureCannotBuyPaidRescue(t *testing.T) {
	upstream := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}},
		{writeBytes: []byte("paid rescue")},
	}}
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "enterprise-seat", AccessToken: "enterprise-token"}}}
	service := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: upstream}).
		WithManagedSubscriptions(leaser).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	ctx := WithManagedSubscriptionCandidates(enterpriseOnlyContext(context.Background()), []*auth.SubscriptionAccount{{
		ID: "enterprise-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive,
	}})
	recorder := httptest.NewRecorder()
	buffer := newPreludeBuffer(recorder)
	in := plannedInputs(recorder, buffer, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
	in.initialDecision = router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}
	_, err := service.dispatchWithFallback(ctx, in)
	require.Error(t, err)
	require.Equal(t, 1, upstream.calls)
	require.NotContains(t, recorder.Body.String(), "paid rescue")
}

func TestClaudeEnterpriseOnlyNeverAuthorizesPaidClaude(t *testing.T) {
	ingress := parityAnthropicIngress()
	svc := ingress.parityService(&parityUpstream{okBody: ingress.upstreamOK(false)})
	observer := usage.NewObserver([]byte("enterprise-test-salt"), time.Hour, time.Now)
	observer.Record(observer.Key([]byte(ingress.token)), usage.Snapshot{OverageInUse: true, UnifiedResetAt: time.Now().Add(time.Hour)})
	svc.WithUsageObserver(observer)
	ctx := svc.withSubscriptionFundingAuthorization(enterpriseOnlyContext(ingress.subCtx()), http.Header{})
	require.Error(t, svc.subscriptionAPIAuthorization(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String()))
	require.NoError(t, svc.subscriptionAPIAuthorization(ctx, providers.ProviderOpenAI, parityUncoveredOpenAI))
	enterpriseTurn := withClaudeEnterpriseTurn(ctx, router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()})
	require.Error(t, svc.subscriptionAPIAuthorization(enterpriseTurn, providers.ProviderOpenAI, parityUncoveredOpenAI), "rescue must not switch a Claude turn to paid OpenAI")
}

func TestClaudeEnterpriseOnlyRefusesPaidRescue(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{
		subErr: upstreamErr(http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error"}}`),
		okBody: ingress.upstreamOK(false),
	}
	service := ingress.quotaService(upstream)
	recorder, request, body := ingress.request(t, false)
	err := ingress.call(service, enterpriseOnlyContext(ingress.subCtx()), body, recorder, request)
	require.Error(t, err)
	require.Positive(t, upstream.subDispatches)
	require.Zero(t, upstream.paidDispatches)
}

func TestClaudeEnterpriseOnlyRequiresClaudeCredential(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{okBody: ingress.upstreamOK(false)}
	service := ingress.parityService(upstream)
	recorder, request, body := ingress.request(t, false)
	err := ingress.call(service, enterpriseOnlyContext(routerKeyedCtx()), body, recorder, request)
	require.Error(t, err)
	require.Zero(t, upstream.subDispatches)
	require.Zero(t, upstream.paidDispatches)
	require.ErrorIs(t, err, ErrClaudeEnterpriseSubscriptionUnavailable)
}

func TestClaudeEnterpriseOnlyBypassRefusalDoesNotReroute(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{
		subErr: upstreamErr(http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error"}}`),
		okBody: ingress.upstreamOK(false),
	}
	service := ingress.quotaService(upstream)
	threshold := 0.9
	ctx := context.WithValue(enterpriseOnlyContext(ingress.subCtx()), InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true, Threshold: &threshold})
	recorder, request, body := ingress.request(t, false)
	require.NoError(t, ingress.call(service, ctx, body, recorder, request))
	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Equal(t, 1, upstream.subDispatches)
	require.Zero(t, upstream.paidDispatches)
}

func TestClaudeEnterpriseOnlyDisabledWithSubscriptionRouting(t *testing.T) {
	ctx := enterpriseOnlyContext(subscriptionDisabledCtx())
	require.False(t, claudeEnterpriseOnly(ctx))
}
