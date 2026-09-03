package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"workweave/router/internal/auth"
	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/subscriptions"
)

type scriptedSubscriptionLeaser struct {
	leases      []subscriptions.Lease
	next        int
	providers   []subscriptions.Provider
	cooldownIDs []string
	disabledIDs []string
}

func (s *scriptedSubscriptionLeaser) Lease(_ context.Context, _ string, provider subscriptions.Provider, _ string) (subscriptions.Lease, bool, error) {
	s.providers = append(s.providers, provider)
	if s.next >= len(s.leases) {
		return subscriptions.Lease{}, true, subscriptions.ErrNoAvailableAccount
	}
	lease := s.leases[s.next]
	s.next++
	return lease, true, nil
}

func (s *scriptedSubscriptionLeaser) Cooldown(_ context.Context, _ string, _ subscriptions.Provider, accountID string, _ time.Time) error {
	s.cooldownIDs = append(s.cooldownIDs, accountID)
	return nil
}

func (s *scriptedSubscriptionLeaser) Disable(_ context.Context, _ string, _ subscriptions.Provider, accountID string) error {
	s.disabledIDs = append(s.disabledIDs, accountID)
	return nil
}

func managedSubscriptionTestContext() context.Context {
	return managedSubscriptionContext(auth.SubscriptionProviderClaude)
}

func managedSubscriptionContext(provider auth.SubscriptionProvider) context.Context {
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "key-1")
	ctx = context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{
		provider: {},
	})
	return WithManagedSubscriptionUsage(ctx)
}

func TestDispatchWithFallbackUsesOnlyMatchingManagedProviderFamily(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "opaque-codex", AccessToken: "token-codex"}}}
	client := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{{writeBytes: []byte("served")}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: client}).WithManagedSubscriptions(leaser)
	recorder := httptest.NewRecorder()
	buffer := newPreludeBuffer(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	_, err := svc.dispatchWithFallback(managedSubscriptionContext(auth.SubscriptionProviderCodex), failoverInputs{
		w: recorder, buf: buffer,
		initialDecision: router.Decision{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI},
		bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}},
		attempt: func(ctx context.Context, decision router.Decision, client providers.Client) error {
			require.Equal(t, "token-codex", string(CredentialsFromContext(ctx).APIKey))
			buffer.Seal()
			return client.Proxy(ctx, decision, providers.PreparedRequest{}, buffer, request)
		},
	})

	require.NoError(t, err)
	require.Equal(t, []subscriptions.Provider{subscriptions.ProviderCodex}, leaser.providers)
	require.Equal(t, "served", recorder.Body.String())
}

func TestDispatchWithFallbackDoesNotCrossManagedProviderFamilies(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "opaque-claude", AccessToken: "token-claude"}}}
	svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser)

	ctx, _, managed, err := svc.leaseManagedSubscription(
		managedSubscriptionContext(auth.SubscriptionProviderClaude),
		providers.ProviderOpenAI,
		"gpt-5.6-sol",
	)

	require.NoError(t, err)
	require.False(t, managed)
	require.Nil(t, CredentialsFromContext(ctx))
	require.Empty(t, leaser.providers)
}

func TestDispatchWithFallbackRotatesManagedAccountBeforeCommit(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{
		{AccountID: "opaque-a", AccessToken: "token-a"},
		{AccountID: "opaque-b", AccessToken: "token-b"},
	}}
	client := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Headers: http.Header{"Retry-After": []string{"30"}}}},
		{writeBytes: []byte("served")},
	}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: client}).WithManagedSubscriptions(leaser)
	recorder := httptest.NewRecorder()
	buffer := newPreludeBuffer(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	var tokens []string

	ctx := managedSubscriptionTestContext()
	_, err := svc.dispatchWithFallback(ctx, failoverInputs{
		w: recorder, buf: buffer,
		initialDecision: router.Decision{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic},
		bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
		attempt: func(ctx context.Context, decision router.Decision, client providers.Client) error {
			tokens = append(tokens, string(CredentialsFromContext(ctx).APIKey))
			buffer.Seal()
			return client.Proxy(ctx, decision, providers.PreparedRequest{}, buffer, request)
		},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"token-a", "token-b"}, tokens)
	require.Equal(t, []string{"opaque-a"}, leaser.cooldownIDs)
	require.Equal(t, "served", recorder.Body.String())
	require.True(t, servedOnSubscription(ctx))
}

func TestDispatchWithFallbackDisablesRejectedManagedAccountBeforeRotation(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{
		{AccountID: "opaque-a", AccessToken: "token-a"},
		{AccountID: "opaque-b", AccessToken: "token-b"},
	}}
	client := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized}},
		{writeBytes: []byte("served")},
	}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: client}).WithManagedSubscriptions(leaser)
	recorder := httptest.NewRecorder()
	buffer := newPreludeBuffer(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	_, err := svc.dispatchWithFallback(managedSubscriptionTestContext(), failoverInputs{
		w: recorder, buf: buffer,
		initialDecision: router.Decision{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic},
		bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
		attempt: func(ctx context.Context, decision router.Decision, client providers.Client) error {
			buffer.Seal()
			return client.Proxy(ctx, decision, providers.PreparedRequest{}, buffer, request)
		},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"opaque-a"}, leaser.disabledIDs)
	require.Equal(t, "served", recorder.Body.String())
}

func TestDispatchWithFallbackDoesNotReplayCommittedManagedStream(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{
		{AccountID: "opaque-a", AccessToken: "token-a"},
		{AccountID: "opaque-b", AccessToken: "token-b"},
	}}
	client := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{
		writeBytes: []byte("partial"), err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests},
	}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: client}).WithManagedSubscriptions(leaser)
	recorder := httptest.NewRecorder()
	buffer := newPreludeBuffer(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	_, err := svc.dispatchWithFallback(managedSubscriptionTestContext(), failoverInputs{
		w: recorder, buf: buffer,
		initialDecision: router.Decision{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic},
		bindings:        []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}},
		attempt: func(ctx context.Context, decision router.Decision, client providers.Client) error {
			buffer.Seal()
			return client.Proxy(ctx, decision, providers.PreparedRequest{}, buffer, request)
		},
	})

	require.Error(t, err)
	require.Equal(t, 1, leaser.next)
	require.Equal(t, "partial", recorder.Body.String())
}

func TestDispatchWithFallbackNeverUsesPaidBindingAfterManagedExhaustion(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}}}
	primary := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}}}}
	paidFallback := &fakeClient{name: "paid"}
	svc := newServiceWithProviders(t, map[string]providers.Client{
		providers.ProviderAnthropic: primary,
		"paid":                      paidFallback,
	}).WithManagedSubscriptions(leaser)
	recorder := httptest.NewRecorder()
	buffer := newPreludeBuffer(recorder)

	_, err := svc.dispatchWithFallback(managedSubscriptionTestContext(), failoverInputs{
		w: recorder, buf: buffer,
		initialDecision: router.Decision{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic},
		bindings: []catalog.ProviderBinding{
			{Provider: providers.ProviderAnthropic},
			{Provider: "paid"},
		},
		attempt: func(ctx context.Context, decision router.Decision, client providers.Client) error {
			buffer.Seal()
			return client.Proxy(ctx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		},
	})

	require.True(t, errors.Is(err, ErrSubscriptionPoolExhausted))
	require.Equal(t, 0, paidFallback.calls)
}
