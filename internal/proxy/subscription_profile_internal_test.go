package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

type subscriptionPlanReaderFunc func(context.Context, []byte) (providers.SubscriptionPlan, error)

func (f subscriptionPlanReaderFunc) SubscriptionPlan(ctx context.Context, token []byte) (providers.SubscriptionPlan, error) {
	return f(ctx, token)
}

func TestEnterprisePolicyUsesVerifiedPlanEvenWithExplicitOrgFlag(t *testing.T) {
	for _, plan := range []providers.SubscriptionPlan{providers.SubscriptionPlanPro, providers.SubscriptionPlanMax, providers.SubscriptionPlanTeam, providers.SubscriptionPlanEnterprise, providers.SubscriptionPlanUnknown} {
		t.Run(string(plan), func(t *testing.T) {
			service := newServiceWithProviders(t, nil).WithClaudeSubscriptionPlanReader(subscriptionPlanReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) { return plan, nil }))
			ctx := flags.WithOverrides(routerKeyedCtx(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyClaudeEnterpriseOnly: true}})
			headers := http.Header{"Authorization": []string{"Bearer " + parityAnthropicToken}}
			ctx = service.withSubscriptionFundingAuthorization(ctx, headers)
			require.Equal(t, plan, claudeSubscriptionPlan(ctx))
			require.Equal(t, plan == providers.SubscriptionPlanEnterprise, claudeEnterpriseOnly(ctx))
		})
	}
}

func TestSubscriptionProfileCacheIsCredentialScopedAndExpires(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	calls := 0
	service := newServiceWithProviders(t, nil).WithClaudeSubscriptionPlanReader(subscriptionPlanReaderFunc(func(_ context.Context, token []byte) (providers.SubscriptionPlan, error) {
		calls++
		if string(token) == "sk-ant-oat01-enterprise" {
			return providers.SubscriptionPlanEnterprise, nil
		}
		return providers.SubscriptionPlanMax, nil
	}))
	service.now = func() time.Time { return now }
	for range 2 {
		for _, token := range []string{"sk-ant-oat01-enterprise", "sk-ant-oat01-ordinary"} {
			ctx := service.withClaudeSubscriptionProfile(routerKeyedCtx(), http.Header{"Authorization": []string{"Bearer " + token}})
			require.Equal(t, token == "sk-ant-oat01-enterprise", claudeEnterpriseOnly(ctx))
		}
	}
	require.Equal(t, 2, calls)
	now = now.Add(6 * time.Minute)
	service.withClaudeSubscriptionProfile(routerKeyedCtx(), http.Header{"Authorization": []string{"Bearer sk-ant-oat01-ordinary"}})
	require.Equal(t, 3, calls)
}

func TestProfileFailureCannotIdentifyEnterpriseOrAuthorizePaidFallback(t *testing.T) {
	service := newServiceWithProviders(t, nil).WithClaudeSubscriptionPlanReader(subscriptionPlanReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) {
		return providers.SubscriptionPlanUnknown, errors.New("synthetic profile unavailable")
	}))
	ctx := context.WithValue(routerKeyedCtx(), AnthropicSubscriptionContextKey{}, parityAnthropicToken)
	ctx = service.withSubscriptionFundingAuthorization(ctx, nil)
	require.False(t, claudeEnterpriseOnly(ctx))
	require.ErrorIs(t, service.subscriptionAPIAuthorization(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String()), ErrSubscriptionPoolUnavailable)
	claudeTurn := withClaudeEnterpriseTurn(ctx, router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()})
	require.ErrorIs(t, service.subscriptionAPIAuthorization(claudeTurn, providers.ProviderGoogle, catalog.ModelIDGemini31ProPreview.String()), ErrSubscriptionPoolUnavailable, "unknown Claude identity cannot authorize paid rescue on another provider")
	require.NoError(t, service.subscriptionAPIAuthorization(ctx, providers.ProviderGoogle, catalog.ModelIDGemini31ProPreview.String()), "an independently selected Google model does not use Claude funding")
}

func TestOrdinaryClaudePlanRetainsPaidRecoveryWithEnterpriseFlag(t *testing.T) {
	for _, plan := range []providers.SubscriptionPlan{providers.SubscriptionPlanPro, providers.SubscriptionPlanMax, providers.SubscriptionPlanTeam} {
		t.Run(string(plan), func(t *testing.T) {
			ingress := parityAnthropicIngress()
			upstream := &parityUpstream{okBody: ingress.upstreamOK(false)}
			service := ingress.quotaService(upstream).WithClaudeSubscriptionPlanReader(subscriptionPlanReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) { return plan, nil }))
			service.usageObserver.Record(service.usageObserver.Key([]byte(ingress.token)), usage.Snapshot{Secondary: usage.Window{UsedPercent: 1, WindowMinutes: 10080, ResetAt: time.Now().Add(time.Hour)}})
			ctx := flags.WithOverrides(ingress.subCtx(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyClaudeEnterpriseOnly: true}})
			recorder, request, body := ingress.request(t, false)

			require.NoError(t, ingress.call(service, ctx, body, recorder, request))
			require.Zero(t, upstream.subDispatches)
			require.Positive(t, upstream.paidDispatches)
		})
	}
}

func TestMixedPoolClassifiesTheLeasedAccount(t *testing.T) {
	for _, enterpriseSeat := range []bool{false, true} {
		t.Run("enterprise-seat="+boolLit(enterpriseSeat), func(t *testing.T) {
			plan := providers.SubscriptionPlanMax
			if enterpriseSeat {
				plan = providers.SubscriptionPlanEnterprise
			}
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "synthetic-seat-token"}}}
			service := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser).
				WithClaudeSubscriptionPlanReader(subscriptionPlanReaderFunc(func(_ context.Context, token []byte) (providers.SubscriptionPlan, error) {
					require.Equal(t, "synthetic-seat-token", string(token))
					return plan, nil
				}))
			ctx := WithManagedSubscriptionCandidates(enterpriseOnlyContext(context.Background()), []*auth.SubscriptionAccount{{ID: "synthetic-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})

			leasedCtx, lease, managed, err := service.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())

			require.NoError(t, err)
			require.True(t, managed)
			require.Equal(t, enterpriseSeat, claudeEnterpriseOnly(leasedCtx), "the org flag and a different caller's plan cannot classify a leased seat")
			lease.Release()
		})
	}
}

func TestSkippedPoolSeatDoesNotSetTheServedPlan(t *testing.T) {
	for _, plan := range []providers.SubscriptionPlan{providers.SubscriptionPlanMax, providers.SubscriptionPlanUnknown} {
		t.Run(string(plan), func(t *testing.T) {
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "spent-seat", AccessToken: "spent-token", State: auth.SubscriptionAccountStateExhausted, CooldownUntil: time.Now().Add(time.Hour)}}}
			service := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser).
				WithClaudeSubscriptionPlanReader(subscriptionPlanReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) { return plan, nil }))
			ctx := managedSubscriptionTestContext()
			_, _, _, err := service.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())
			require.ErrorIs(t, err, ErrSubscriptionPoolExhausted)
			state := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
			require.Empty(t, state.ClaudeSubscriptionPlan, "a seat excluded before dispatch cannot become the request's served plan")
			require.Equal(t, plan == providers.SubscriptionPlanUnknown, state.ClaudePlanUnverified)
		})
	}
}

func TestEnterpriseSeatDoesNotMarkAnIndependentNonClaudeTurn(t *testing.T) {
	ctx := WithManagedSubscriptionUsage(context.Background())
	state := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	state.ClaudeSubscriptionPlan = providers.SubscriptionPlanEnterprise
	nonClaude := withClaudeEnterpriseTurn(ctx, router.Decision{Provider: providers.ProviderGoogle, Model: catalog.ModelIDGemini31ProPreview.String()})
	require.False(t, claudeEnterpriseTurn(nonClaude))
	claude := withClaudeEnterpriseTurn(ctx, router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()})
	require.True(t, claudeEnterpriseTurn(claude))
}
