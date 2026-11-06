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
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/subscriptions"
)

func enterpriseOnlyContext(ctx context.Context) context.Context {
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

func TestClaudeEnterpriseOnlyDefaultsToLinkedAccounts(t *testing.T) {
	ctx := context.WithValue(routerKeyedCtx(), AnthropicSubscriptionContextKey{}, parityAnthropicToken)
	require.True(t, claudeEnterpriseOnly(ctx))
	managed := context.WithValue(routerKeyedCtx(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
	require.True(t, claudeEnterpriseOnly(managed))
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
	require.True(t, claudeEnterpriseOnly(headerOnly), "native header credentials have the same default as registered accounts")
}

func TestClaudeEnterpriseOnlyClassifiersKeepOverage(t *testing.T) {
	ctx := context.WithValue(routerKeyedCtx(), AnthropicSubscriptionContextKey{}, classifierTestSubToken)
	service := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: &parityUpstream{}}).WithUsageObserver(observerWithSnapshot(classifierTestSubToken, usage.Snapshot{OverageInUse: true}))
	_, engaged := service.classifierPassthroughEngaged(ctx, http.Header{}, router.Request{RequestedModel: catalog.ModelIDClaudeSonnet46.String()}, turntype.Classifier)
	require.True(t, engaged)
}

func TestClaudeEnterpriseOnlyKeepsQuotaAndOverageOnOAuth(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, snapshot := range []usage.Snapshot{
			{Primary: usage.Window{UsedPercent: 0.5, WindowMinutes: 300}},
			{Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300}},
			{OverageInUse: true, QuotaWindowsSeen: true},
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
		WithUsageObserver(observerWithSnapshot("enterprise-token", usage.Snapshot{OverageInUse: true, QuotaWindowsSeen: true}))
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

func TestClaudeEnterpriseOnlyDefaultMovesFromIncludedUsageToOverage(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{okBody: ingress.upstreamOK(false), subHeaders: http.Header{
		"Anthropic-Ratelimit-Unified-Representative-Claim": []string{string(usage.AnthropicClaimOverage)},
		"Anthropic-Ratelimit-Unified-Overage-In-Use":       []string{"true"},
	}}
	service := ingress.quotaService(upstream)
	service.usageObserver.Record(service.usageObserver.Key([]byte(ingress.token)), usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.5, WindowMinutes: 300},
	})
	ctx := context.WithValue(routerKeyedCtx(), AnthropicSubscriptionContextKey{}, ingress.token)
	for range 2 {
		recorder, request, body := ingress.request(t, false)
		require.NoError(t, ingress.call(service, ctx, body, recorder, request))
		snapshot, observed := service.usageObserver.Snapshot(service.usageObserver.Key([]byte(ingress.token)))
		require.True(t, observed)
		require.True(t, snapshot.OverageInUse)
		require.True(t, snapshot.QuotaWindowsSeen)
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

type routedModelRecorder struct{ routed *[]string }

func (r routedModelRecorder) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	decision, err := stateModelRouter{}.Route(ctx, req)
	if err == nil {
		*r.routed = append(*r.routed, decision.Model)
	}
	return decision, err
}

func TestClaudeEnterpriseOnlyDefaultSubscriptionStateRefusal(t *testing.T) {
	rateLimitBody := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
	planRefusal := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: rateLimitBody, Headers: http.Header{
		"Anthropic-Ratelimit-Unified-Representative-Claim": []string{string(usage.AnthropicClaimSevenDay)},
		"Anthropic-Ratelimit-Unified-7d-Utilization":       []string{"1.0"},
		"Anthropic-Ratelimit-Unified-7d-Reset":             []string{"4102444800"},
		"Anthropic-Ratelimit-Unified-Overage-Status":       []string{"rejected"},
	}}
	usageBilledRefusal := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: rateLimitBody, Headers: http.Header{"Retry-After": []string{"30"}}}
	for _, scenario := range []struct {
		name        string
		refusal     error
		codexLinked bool
		want        string
	}{
		{name: "spent plan seat rotates to included Codex", refusal: planRefusal, codexLinked: true, want: stateCodexModel},
		{name: "spent plan seat uses the paid set", refusal: planRefusal, want: statePaidModel},
		{name: "usage-billed seat returns its rate limit without a denied rescue walk", refusal: usageBilledRefusal, codexLinked: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			claude := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{err: scenario.refusal}, {err: scenario.refusal}, {err: scenario.refusal}}}
			codex := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{{writeBytes: []byte("included Codex answer")}}}
			paid := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("paid answer")}}}
			var routed []string
			svc := NewService(routedModelRecorder{routed: &routed}, map[string]providers.Client{providers.ProviderAnthropic: claude, providers.ProviderOpenAI: codex, providers.ProviderOpenRouter: paid}, nil, false, nil, nil, false, "", "", nil).
				WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true, subscriptions.ProviderCodex: scenario.codexLinked}})
			ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "key-1")
			ctx = context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
			ctx = WithManagedSubscriptionUsage(ctx)
			require.True(t, claudeEnterpriseOnly(ctx), "linked accounts inherit the Enterprise-only default")
			ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel, stateCodexModel})
			ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
			ctx = withTurnFailures(ctx)
			req := router.Request{AllowedModels: allowedModelsForRequest(ctx), EnabledProviders: modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderOpenRouter})}
			rec := httptest.NewRecorder()
			buffer := newPreludeBuffer(rec)
			var winner router.Decision
			dispatch := failoverInputs{
				w: rec, buf: buffer, subscriptionStateRequest: &req,
				subscriptionStateWinnerProvider: new(string),
				initialDecision:                 router.Decision{Model: stateClaudeModel, Provider: providers.ProviderAnthropic},
				purpose:                         inference.PurposeAnthropicMessages,
				buildAlternative: func(router.Decision) (dispatchAttempt, error) {
					return func(attemptCtx context.Context, decision router.Decision, client providers.Client) error {
						buffer.Seal()
						return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
					}, nil
				},
				onAlternative: func(decision router.Decision) { winner = decision },
			}

			_, err := svc.dispatchWithFallback(ctx, dispatch)

			if scenario.want == "" {
				require.Error(t, err)
				class, ok := ClassifyDispatchError(err)
				require.True(t, ok)
				require.Equal(t, http.StatusTooManyRequests, class.Status)
				require.NotContains(t, routed, stateCodexModel, "Enterprise-only denies every non-Claude target")
				require.NotContains(t, routed, statePaidModel, "Enterprise-only denies every non-Claude target")
				require.Zero(t, codex.calls+paid.calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, scenario.want, winner.Model)
			require.Contains(t, rec.Body.String(), "answer")
		})
	}
}

func TestClaudeEnterpriseOnlyDefaultSkipsObservedSpentPlanSeat(t *testing.T) {
	spent := usage.Snapshot{Secondary: usage.Window{UsedPercent: 1, WindowMinutes: 7 * 24 * 60, ResetAt: time.Now().Add(time.Hour)}}
	for _, scenario := range []struct {
		name      string
		snapshot  usage.Snapshot
		optedIn   bool
		wantLease bool
	}{
		{name: "spent plan seat releases the default", snapshot: spent},
		{name: "plan seat on overage stays leased", snapshot: usage.Snapshot{OverageInUse: true, Secondary: spent.Secondary}, wantLease: true},
		{name: "explicit opt-in keeps the spent seat", snapshot: spent, optedIn: true, wantLease: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			leaser := &healthSubscriptionLeaser{scriptedSubscriptionLeaser: &scriptedSubscriptionLeaser{
				leases: []subscriptions.Lease{{AccountID: "plan-seat", AccessToken: "plan-token"}},
			}}
			svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser).
				WithUsageObserver(observerWithSnapshot("plan-token", scenario.snapshot))
			ctx := WithManagedSubscriptionUsage(context.WithValue(context.WithValue(context.Background(), APIKeyIDContextKey{}, "key-1"),
				ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}}))
			if scenario.optedIn {
				ctx = enterpriseOnlyContext(ctx)
			}
			_, lease, _, _ := svc.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())
			if scenario.wantLease {
				require.Equal(t, "plan-seat", lease.AccountID)
				require.True(t, claudeEnterpriseOnly(ctx))
				lease.Release()
				return
			}
			require.Empty(t, lease.AccountID)
			require.Equal(t, []string{"plan-seat"}, leaser.exhaustedIDs)
			require.False(t, claudeEnterpriseOnly(ctx), "a plan seat that cannot serve releases the inherited default")
		})
	}
}

func TestClaudeEnterpriseOnlyDefaultKeepsSiblingOverageSeat(t *testing.T) {
	leaser := &healthSubscriptionLeaser{scriptedSubscriptionLeaser: &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{
		{AccountID: "spent-seat", AccessToken: "spent-token"},
		{AccountID: "overage-seat", AccessToken: "overage-token"},
	}}}
	observer := usage.NewObserver([]byte("enterprise-test-salt"), time.Hour, time.Now)
	weekly := usage.Window{UsedPercent: 1, WindowMinutes: 7 * 24 * 60, ResetAt: time.Now().Add(time.Hour)}
	observer.Record(observer.Key([]byte("spent-token")), usage.Snapshot{Secondary: weekly})
	observer.Record(observer.Key([]byte("overage-token")), usage.Snapshot{Secondary: weekly, OverageInUse: true})
	svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser).WithUsageObserver(observer)
	ctx := WithManagedSubscriptionUsage(context.WithValue(context.WithValue(context.Background(), APIKeyIDContextKey{}, "key-1"),
		ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}}))

	_, lease, managed, err := svc.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())

	require.NoError(t, err)
	require.True(t, managed)
	require.Equal(t, "overage-seat", lease.AccountID, "a sibling on provider-authorized overage still serves before any fallback")
	require.Equal(t, []string{"spent-seat"}, leaser.exhaustedIDs)
	lease.Release()
}
