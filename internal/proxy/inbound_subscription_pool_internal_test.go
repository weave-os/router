package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

func TestInboundClaudeOAuthStillRotatesManagedPool(t *testing.T) {
	for _, stateModels := range []bool{false, true} {
		t.Run("state-models="+boolLit(stateModels), func(t *testing.T) {
			var authorizations []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorizations = append(authorizations, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("Authorization") != "Bearer synthetic-healthy-seat" {
					w.Header().Set("Retry-After", "30")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"synthetic account rate limit"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"synthetic-message","type":"message","role":"assistant","model":"`+catalog.ModelIDClaudeOpus48.String()+`","content":[{"type":"text","text":"pooled answer"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":7}}`)
			}))
			defer upstream.Close()
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{
				{AccountID: "synthetic-limited-seat", AccessToken: "synthetic-limited-seat"},
				{AccountID: "synthetic-healthy-seat", AccessToken: "synthetic-healthy-seat"},
			}}
			client := anthropic.NewClient("synthetic-paid-key", upstream.URL)
			service := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}}, map[string]providers.Client{providers.ProviderAnthropic: client}, nil, false, nil, nil, false, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String(), nil).
				WithManagedSubscriptions(leaser).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
			service.retrySleep = noopSleep
			ctx := enterpriseOnlyContext(managedSubscriptionTestContext())
			ctx = context.WithValue(ctx, InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
			ctx = context.WithValue(ctx, InstallationSubscriptionStateModelsEnabledContextKey{}, stateModels)
			body := `{"model":"auto","max_tokens":1024,"messages":[{"role":"user","content":"inspect synthetic input"}],"tools":[{"name":"read_file","description":"read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+parityAnthropicToken)
			recorder := httptest.NewRecorder()

			err := service.ProxyMessages(ctx, []byte(body), recorder, request)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), "pooled answer")
			require.Equal(t, []string{"Bearer synthetic-limited-seat", "Bearer synthetic-healthy-seat"}, authorizations)
			require.Equal(t, []string{"synthetic-limited-seat"}, leaser.cooldownIDs)
			require.True(t, managedSubscriptionServed(ctx))
		})
	}
}

func TestResolvedOAuthUsesEnrolledPool(t *testing.T) {
	for _, scenario := range []struct {
		provider             string
		subscriptionProvider auth.SubscriptionProvider
		model                string
		source               string
	}{
		{providers.ProviderAnthropic, auth.SubscriptionProviderClaude, catalog.ModelIDClaudeOpus48.String(), credSourceSubscription},
		{providers.ProviderOpenAI, auth.SubscriptionProviderCodex, codexCoveredModel, credSourceCodexSubscription},
	} {
		t.Run(scenario.provider, func(t *testing.T) {
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "synthetic-managed-token"}}}
			service := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser)
			ctx := context.WithValue(managedSubscriptionContext(scenario.subscriptionProvider), CredentialsContextKey{}, &Credentials{APIKey: []byte("synthetic-inbound-token"), OAuth: true, Source: scenario.source})

			pooled, lease, managed, err := service.leaseManagedSubscription(ctx, scenario.provider, scenario.model)

			require.NoError(t, err)
			require.True(t, managed)
			require.Equal(t, "synthetic-seat", lease.AccountID)
			require.Equal(t, "synthetic-managed-token", string(CredentialsFromContext(pooled).APIKey))
			lease.Release()
		})
	}
}

type failedPoolLeaser struct {
	scriptedSubscriptionLeaser
	err error
}

func (l *failedPoolLeaser) Lease(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string) (subscriptions.Lease, bool, error) {
	return subscriptions.Lease{}, true, l.err
}

func TestResolvedOAuthSurvivesPoolLeaseFailure(t *testing.T) {
	for _, scenario := range []struct {
		provider             string
		subscriptionProvider auth.SubscriptionProvider
		model                string
		source               string
	}{
		{providers.ProviderAnthropic, auth.SubscriptionProviderClaude, catalog.ModelIDClaudeOpus48.String(), credSourceSubscription},
		{providers.ProviderOpenAI, auth.SubscriptionProviderCodex, codexCoveredModel, credSourceCodexSubscription},
	} {
		for _, canceled := range []bool{false, true} {
			t.Run(scenario.provider+"/canceled="+boolLit(canceled), func(t *testing.T) {
				leaseErr := errors.New("synthetic pool database unavailable")
				if canceled {
					leaseErr = context.Canceled
				}
				service := newServiceWithProviders(t, nil).WithManagedSubscriptions(&failedPoolLeaser{err: leaseErr})
				oauth := &Credentials{APIKey: []byte("synthetic-inbound-token"), OAuth: true, Source: scenario.source}
				ctx := enterpriseOnlyContext(managedSubscriptionContext(scenario.subscriptionProvider))
				ctx, cancel := context.WithCancel(context.WithValue(ctx, CredentialsContextKey{}, oauth))
				defer cancel()
				if canceled {
					cancel()
				}

				direct, lease, managed, err := service.leaseManagedSubscription(ctx, scenario.provider, scenario.model)

				if canceled {
					require.ErrorIs(t, err, context.Canceled)
					require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable)
					return
				}
				require.NoError(t, err)
				require.False(t, managed)
				require.Empty(t, lease.AccountID)
				require.Same(t, oauth, CredentialsFromContext(direct))
			})
		}
	}
}

func TestResolvedOAuthSurvivesOutOfSnapshotLease(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "synthetic-new-seat", AccessToken: "synthetic-new-token"}}}
	service := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser)
	ctx := WithManagedSubscriptionCandidates(enterpriseOnlyContext(managedSubscriptionTestContext()), []*auth.SubscriptionAccount{{
		ID: "synthetic-admitted-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive,
	}})
	oauth := &Credentials{APIKey: []byte(parityAnthropicToken), OAuth: true, Source: credSourceSubscription}
	ctx = context.WithValue(ctx, CredentialsContextKey{}, oauth)

	direct, lease, managed, err := service.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())

	require.NoError(t, err)
	require.False(t, managed)
	require.Empty(t, lease.AccountID)
	require.Same(t, oauth, CredentialsFromContext(direct), "an out-of-snapshot seat cannot displace the caller's own login")
}

func TestResolvedOAuthRemainsAvailableWithoutManagedCapacity(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		adjust        func(context.Context) context.Context
		availableSeat bool
	}{
		{"no enrolled pool", func(ctx context.Context) context.Context {
			return context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{})
		}, false},
		{"no available seat", func(ctx context.Context) context.Context { return ctx }, false},
		{"enrollment unavailable", func(ctx context.Context) context.Context {
			return context.WithValue(ctx, ManagedSubscriptionEnrollmentUnavailableContextKey{}, true)
		}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			leaser := &scriptedSubscriptionLeaser{}
			if scenario.availableSeat {
				leaser.leases = []subscriptions.Lease{{AccountID: "synthetic-seat", AccessToken: "synthetic-managed-token"}}
			}
			service := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser)
			oauth := &Credentials{APIKey: []byte(parityAnthropicToken), OAuth: true, Source: credSourceSubscription}
			ctx := scenario.adjust(context.WithValue(managedSubscriptionTestContext(), CredentialsContextKey{}, oauth))

			direct, lease, managed, err := service.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())

			require.NoError(t, err)
			require.False(t, managed)
			require.Empty(t, lease.AccountID)
			require.Same(t, oauth, CredentialsFromContext(direct))
		})
	}
}
