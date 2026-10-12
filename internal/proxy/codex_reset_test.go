package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
)

type resetSubscriptionLeaser struct {
	*scriptedSubscriptionLeaser
	resets    int
	recovered subscriptions.Lease
}

func (l *resetSubscriptionLeaser) ResetCodex(context.Context, auth.SubscriptionOwner, string) (subscriptions.Lease, bool, error) {
	l.resets++
	return l.recovered, l.recovered.AccountID != "", nil
}

func TestManagedCodexResetIsOptInAndPreservesSubscriptionModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		scope   func(context.Context) context.Context
		want    int
	}{
		{name: "default off"},
		{name: "enabled", enabled: true, want: 1},
		{name: "disabled subscription routing", enabled: true, scope: func(ctx context.Context) context.Context {
			return context.WithValue(ctx, InstallationSubscriptionRoutingDisabledContextKey{}, true)
		}},
		{name: "max", enabled: true, scope: func(ctx context.Context) context.Context {
			return entitlement.WithProductScope(ctx, entitlement.PlanMax)
		}},
		{name: "API only fallback", enabled: true, scope: func(ctx context.Context) context.Context {
			return context.WithValue(ctx, subscriptionAPIOnlyKey{}, true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaser := &resetSubscriptionLeaser{scriptedSubscriptionLeaser: &scriptedSubscriptionLeaser{}, recovered: subscriptions.Lease{AccountID: "reset-account", AccessToken: "restored-token"}}
			svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: &fakeClient{name: providers.ProviderOpenAI}}).WithManagedSubscriptions(leaser)
			ctx := managedSubscriptionContext(auth.SubscriptionProviderCodex)
			ctx = WithSubscriptionOwner(ctx, auth.SubscriptionOwner{SubscriberID: "subscriber-1"})
			if tc.enabled {
				ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyCodexAutoUsageReset: true}})
			}
			if tc.scope != nil {
				ctx = tc.scope(ctx)
			}
			out, lease, _, err := svc.leaseManagedSubscription(ctx, providers.ProviderOpenAI, "gpt-6.1-sol")
			require.Equal(t, tc.want, leaser.resets)
			if tc.want == 1 {
				require.NoError(t, err)
				require.Equal(t, "reset-account", lease.AccountID)
				require.Equal(t, "restored-token", string(CredentialsFromContext(out).APIKey))
				_, _, _, _ = svc.leaseManagedSubscription(ctx, providers.ProviderOpenAI, "gpt-6.1-sol")
				require.Equal(t, 1, leaser.resets, "at most one reset attempt per request")
			}
		})
	}
}

func TestManagedCodexResetClearsStaleExhaustionAndRetriesSameAccount(t *testing.T) {
	account := subscriptions.Lease{AccountID: "reset-account", AccessToken: "token", ProviderAccount: "workspace"}
	leaser := &resetSubscriptionLeaser{scriptedSubscriptionLeaser: &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{account}}, recovered: account}
	observer := observerWithSnapshot(account.AccessToken, exhaustedSnapshot())
	observer.Record(observer.Key([]byte("subscription-account:"+account.AccountID)), exhaustedSnapshot())
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: &fakeClient{name: providers.ProviderOpenAI}}).WithManagedSubscriptions(leaser).WithUsageObserver(observer)
	ctx := flags.WithOverrides(managedSubscriptionContext(auth.SubscriptionProviderCodex), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyCodexAutoUsageReset: true}})
	ctx = WithSubscriptionOwner(ctx, auth.SubscriptionOwner{SubscriberID: "subscriber-1"})
	attribution := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	attribution.AttemptedAccounts = map[string]struct{}{account.AccountID + "\x00gpt-6.1-sol": {}}
	_, lease, _, err := svc.leaseManagedSubscription(ctx, providers.ProviderOpenAI, "gpt-6.1-sol")
	require.NoError(t, err)
	require.Equal(t, account.AccountID, lease.AccountID)
	require.Equal(t, 1, leaser.resets)
	require.Empty(t, attribution.AttemptedAccounts)
	_, found := svc.managedSubscriptionUsageSnapshot(account.AccountID, account.AccessToken)
	require.False(t, found)
}
