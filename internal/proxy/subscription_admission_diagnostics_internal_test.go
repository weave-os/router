package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

func TestSubscriptionAdmissionReportsSkipGateAndPreservesRotation(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	model := catalog.ModelIDClaudeOpus48.String()
	for _, scenario := range []struct {
		name      string
		reason    subscriptionAccountSkipReason
		configure func(*Service, context.Context, *scriptedSubscriptionLeaser) context.Context
	}{
		{name: "quota", reason: subscriptionSkipQuotaExhausted, configure: func(s *Service, ctx context.Context, _ *scriptedSubscriptionLeaser) context.Context {
			s.WithUsageObserver(observerWithSnapshot("private-access-token", exhaustedSnapshot()))
			return ctx
		}},
		{name: "overage", reason: subscriptionSkipPaidOverage, configure: func(s *Service, ctx context.Context, _ *scriptedSubscriptionLeaser) context.Context {
			s.WithUsageObserver(observerWithSnapshot("private-access-token", usage.Snapshot{OverageInUse: true}))
			return ctx
		}},
		{name: "model denial", reason: subscriptionSkipModelDenied, configure: func(s *Service, ctx context.Context, _ *scriptedSubscriptionLeaser) context.Context {
			s.subscriptionModels.denyManaged("account:skipped", "skipped", providers.ProviderAnthropic, model, now.Add(time.Minute))
			return ctx
		}},
		{name: "overload", reason: subscriptionSkipOverloadCooldown, configure: func(s *Service, ctx context.Context, _ *scriptedSubscriptionLeaser) context.Context {
			s.subscriptionModels.denyManaged(managedOverloadOwner("skipped"), "skipped", providers.ProviderAnthropic, model, now.Add(time.Minute))
			return ctx
		}},
		{name: "client restriction", reason: subscriptionSkipClientAppDenied, configure: func(s *Service, ctx context.Context, _ *scriptedSubscriptionLeaser) context.Context {
			s.subscriptionModels.denyManagedClientApp("skipped", providers.ProviderAnthropic, ClientIdentityFrom(ctx).ClientApp, now.Add(time.Minute))
			return ctx
		}},
		{name: "plan", reason: subscriptionSkipPlanIneligible, configure: func(_ *Service, ctx context.Context, l *scriptedSubscriptionLeaser) context.Context {
			l.leases[0].State = auth.SubscriptionAccountStateExhausted
			l.leases[0].CooldownUntil = now.Add(time.Hour)
			return ctx
		}},
		{name: "admission snapshot", reason: subscriptionSkipOutsideAdmission, configure: func(_ *Service, ctx context.Context, _ *scriptedSubscriptionLeaser) context.Context {
			return WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: "winner", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}})
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var output bytes.Buffer
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "skipped", AccessToken: "private-access-token", Tier: auth.SubscriptionTierShared}, {AccountID: "winner", AccessToken: "winner-token"}}}
			svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser)
			svc.now = func() time.Time { return now }
			ctx := scenario.configure(svc, managedSubscriptionTestContext(), leaser)
			ctx = observability.WithLogger(ctx, slog.New(slog.NewJSONHandler(&output, nil)).With("request_id", "synthetic-request"))
			_, lease, managed, err := svc.leaseManagedSubscription(ctx, providers.ProviderAnthropic, model)
			require.NoError(t, err)
			require.True(t, managed)
			require.Equal(t, "winner", lease.AccountID)
			lease.Release()
			require.Contains(t, leaser.owners[1].ExcludedAccountIDs, "skipped")
			var event struct {
				Event     auth.SubscriptionCredentialEventKind  `json:"subscription_event"`
				RequestID string                                `json:"request_id"`
				Provider  subscriptions.Provider                `json:"provider"`
				Model     string                                `json:"model"`
				Admitted  bool                                  `json:"account_admitted"`
				Skipped   int                                   `json:"skipped_count"`
				Reasons   map[subscriptionAccountSkipReason]int `json:"skip_reasons"`
				Accounts  []subscriptionAccountSkip             `json:"skipped_accounts"`
				Elapsed   *int64                                `json:"admission_ms"`
			}
			var summaries int
			for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
				var kind struct {
					Event auth.SubscriptionCredentialEventKind `json:"subscription_event"`
				}
				require.NoError(t, json.Unmarshal(line, &kind))
				if kind.Event == auth.SubscriptionCredentialEventAdmissionEvaluated {
					summaries++
					require.NoError(t, json.Unmarshal(line, &event))
				}
			}
			require.Equal(t, 1, summaries)
			require.Equal(t, auth.SubscriptionCredentialEventAdmissionEvaluated, event.Event)
			require.Equal(t, "synthetic-request", event.RequestID)
			require.Equal(t, subscriptions.ProviderClaude, event.Provider)
			require.Equal(t, model, event.Model)
			require.True(t, event.Admitted)
			require.Equal(t, 1, event.Skipped)
			require.Equal(t, map[subscriptionAccountSkipReason]int{scenario.reason: 1}, event.Reasons)
			require.Equal(t, []subscriptionAccountSkip{{AccountID: "skipped", Tier: auth.SubscriptionTierShared, Reason: scenario.reason}}, event.Accounts)
			require.NotNil(t, event.Elapsed)
			require.GreaterOrEqual(t, *event.Elapsed, int64(0))
			require.NotContains(t, output.String(), "private-access-token")
			require.NotContains(t, output.String(), "winner-token")
		})
	}
}

func TestSubscriptionAdmissionBoundsSkippedAccountsAndOmitsUnknownModel(t *testing.T) {
	var output bytes.Buffer
	var leases []subscriptions.Lease
	for i := range 20 {
		leases = append(leases, subscriptions.Lease{AccountID: fmt.Sprintf("seat-%d", i), AccessToken: "private-access-token"})
	}
	svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: leases}).WithUsageObserver(observerWithSnapshot("private-access-token", usage.Snapshot{OverageInUse: true}))
	ctx := observability.WithLogger(managedSubscriptionTestContext(), slog.New(slog.NewJSONHandler(&output, nil)))
	_, lease, managed, err := svc.leaseManagedSubscription(ctx, providers.ProviderAnthropic, "caller-controlled-secret")
	require.ErrorIs(t, err, ErrSubscriptionPoolExhausted)
	require.True(t, managed)
	require.Empty(t, lease.AccountID)
	var event struct {
		Skipped   int                                   `json:"skipped_count"`
		Reasons   map[subscriptionAccountSkipReason]int `json:"skip_reasons"`
		Accounts  []subscriptionAccountSkip             `json:"skipped_accounts"`
		Truncated bool                                  `json:"skipped_accounts_truncated"`
		Admitted  bool                                  `json:"account_admitted"`
		Model     string                                `json:"model"`
	}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event))
	require.Equal(t, 20, event.Skipped)
	require.Equal(t, 20, event.Reasons[subscriptionSkipPaidOverage])
	require.Len(t, event.Accounts, 16)
	require.True(t, event.Truncated)
	require.False(t, event.Admitted)
	require.Empty(t, event.Model)
	require.NotContains(t, output.String(), "private-access-token")
	require.NotContains(t, output.String(), "caller-controlled-secret")
}

func TestSubscriptionAdmissionDoesNotLogUnskippedLease(t *testing.T) {
	var output bytes.Buffer
	svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "winner", AccessToken: "private-access-token"}}})
	ctx := observability.WithLogger(managedSubscriptionTestContext(), slog.New(slog.NewJSONHandler(&output, nil)))
	_, lease, managed, err := svc.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())
	require.NoError(t, err)
	require.True(t, managed)
	require.Equal(t, "winner", lease.AccountID)
	lease.Release()
	require.Empty(t, output.String())
}

func TestSubscriptionAdmissionReportsRepeatedLeaseWithoutLooping(t *testing.T) {
	var output bytes.Buffer
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "repeated-seat", AccessToken: "private-access-token"}}, repeatLast: true}
	svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser).WithUsageObserver(observerWithSnapshot("private-access-token", usage.Snapshot{OverageInUse: true}))
	ctx := observability.WithLogger(managedSubscriptionTestContext(), slog.New(slog.NewJSONHandler(&output, nil)))
	_, lease, managed, err := svc.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())
	require.ErrorIs(t, err, ErrSubscriptionPoolExhausted)
	require.True(t, managed)
	require.Empty(t, lease.AccountID)
	require.Len(t, leaser.owners, 2)
	var event struct {
		Reasons map[subscriptionAccountSkipReason]int `json:"skip_reasons"`
		Skipped int                                   `json:"skipped_count"`
	}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event))
	require.Equal(t, 2, event.Skipped)
	require.Equal(t, map[subscriptionAccountSkipReason]int{subscriptionSkipPaidOverage: 1, subscriptionSkipRepeatedLease: 1}, event.Reasons)
}

func TestSubscriptionAdmissionReportsCodexModelDenial(t *testing.T) {
	var output bytes.Buffer
	model := catalog.ModelGPT6Luna
	now := time.Unix(1_000_000, 0)
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "codex-denied", AccessToken: "private-access-token"}, {AccountID: "codex-winner", AccessToken: "winner-token"}}}
	svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser)
	svc.now = func() time.Time { return now }
	svc.subscriptionModels.denyManaged("account:codex-denied", "codex-denied", providers.ProviderOpenAI, model, now.Add(time.Minute))
	ctx := observability.WithLogger(managedSubscriptionContext(auth.SubscriptionProviderCodex), slog.New(slog.NewJSONHandler(&output, nil)))
	_, lease, managed, err := svc.leaseManagedSubscription(ctx, providers.ProviderOpenAI, model)
	require.NoError(t, err)
	require.True(t, managed)
	require.Equal(t, "codex-winner", lease.AccountID)
	lease.Release()
	var event struct {
		Provider subscriptions.Provider    `json:"provider"`
		Model    string                    `json:"model"`
		Accounts []subscriptionAccountSkip `json:"skipped_accounts"`
	}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event))
	require.Equal(t, subscriptions.ProviderCodex, event.Provider)
	require.Equal(t, model, event.Model)
	require.Equal(t, []subscriptionAccountSkip{{AccountID: "codex-denied", Reason: subscriptionSkipModelDenied}}, event.Accounts)
}
