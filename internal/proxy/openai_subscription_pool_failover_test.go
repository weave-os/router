package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

func claudeOpusPinnedDecision() router.Decision {
	return router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-5",
		Reason:   translate.ReasonLoopEscalation,
		Metadata: &router.RoutingMetadata{
			CandidateModels:    []string{"claude-opus-5", "gpt-5.6-luna"},
			CandidateProviders: map[string]string{"gpt-5.6-luna": providers.ProviderOpenAI},
		},
	}
}

func openaiChatBody() []byte {
	return []byte(`{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
}

func anthropicMessagesBody() []byte {
	return []byte(`{"model":"claude-opus-5","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"read main.go"}],"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
}

func TestProxyOpenAIChatCompletion_SubscriptionPoolExhaustionRescuesSibling(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{}
	rescue := &responsesRetryClient{}
	store := &evictionStubPinStore{}
	svc := NewService(
		staticRouter{decision: claudeOpusPinnedDecision()},
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic},
			providers.ProviderOpenAI:    rescue,
		},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{
			providers.ProviderAnthropic: {},
			providers.ProviderOpenAI:    {},
		})

	installationID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderClaude),
		InstallationIDContextKey{}, installationID.String())
	rec := httptest.NewRecorder()
	body := openaiChatBody()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))

	err := svc.ProxyOpenAIChatCompletion(ctx, body, rec, req)

	require.NoError(t, err, "an empty Claude pool must fail over to a same-cluster sibling")
	require.NotEmpty(t, rescue.endpoints, "the OpenAI sibling must be dispatched")
	assert.Contains(t, rec.Body.String(), "served after retry")
}

func TestProxyOpenAIChatCompletion_SubscriptionOnlyKeepsPoolExhaustion(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{}
	rescue := &responsesRetryClient{}
	svc := NewService(
		staticRouter{decision: claudeOpusPinnedDecision()},
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic},
			providers.ProviderOpenAI:    rescue,
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{
			providers.ProviderAnthropic: {},
			providers.ProviderOpenAI:    {},
		})

	ctx := billing.WithSubscriptionOnly(managedSubscriptionContext(auth.SubscriptionProviderClaude), billing.SubscriptionOnlyCreditsDepleted)
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, "33333333-3333-3333-3333-333333333333")
	rec := httptest.NewRecorder()
	body := openaiChatBody()

	err := svc.ProxyOpenAIChatCompletion(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))

	require.ErrorIs(t, err, ErrSubscriptionPoolExhausted)
	assert.Empty(t, rescue.endpoints, "subscription-only must not spend a paid sibling")
}

func TestProxyMessages_SubscriptionPoolExhaustionRescuesSibling(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{}
	rescue := &responsesRetryClient{}
	svc := NewService(
		staticRouter{decision: claudeOpusPinnedDecision()},
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeClient{name: providers.ProviderAnthropic},
			providers.ProviderOpenAI:    rescue,
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithManagedSubscriptions(leaser).
		WithDeploymentKeyedProviders(map[string]struct{}{
			providers.ProviderAnthropic: {},
			providers.ProviderOpenAI:    {},
		})

	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderClaude),
		InstallationIDContextKey{}, "33333333-3333-3333-3333-333333333333")
	rec := httptest.NewRecorder()
	body := anthropicMessagesBody()

	err := svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))

	require.NoError(t, err)
	require.NotEmpty(t, rescue.endpoints)
	assert.Contains(t, rec.Body.String(), "served after retry")
}

func TestMaybeExpireSubscriptionArmPin_StickyLoopEscalation(t *testing.T) {
	store := &evictionStubPinStore{}
	svc := newEvictionTestService(store)
	installationID := uuid.New()
	sessionKey := nonZeroSessionKey()

	svc.maybeExpireSubscriptionArmPin(context.Background(), ErrSubscriptionPoolExhausted, translate.ReasonLoopEscalation, installationID, sessionKey, sessionpin.DefaultRole)

	require.Len(t, store.upserts, 1)
	assert.Equal(t, pinEvictionReasonSubscriptionPool, store.upserts[0].Reason)
}

func TestProxyEndpoints_CachedManagedModelDenialEvictsAutomaticPin(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		run  func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{
			name: "Anthropic messages",
			body: anthropicMessagesBody(),
			run: func(svc *Service, ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
				return svc.ProxyMessages(ctx, body, w, r)
			},
		},
		{
			name: "OpenAI chat",
			body: openaiChatBody(),
			run: func(svc *Service, ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
				return svc.ProxyOpenAIChatCompletion(ctx, body, w, r)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			leaser := &scriptedSubscriptionLeaser{
				leases:     []subscriptions.Lease{{AccountID: "opaque-claude", AccessToken: "managed-token"}},
				repeatLast: true,
			}
			upstream := &fakeClient{name: providers.ProviderAnthropic}
			store := &evictionStubPinStore{}
			svc := NewService(
				staticRouter{decision: claudeOpusPinnedDecision()},
				map[string]providers.Client{providers.ProviderAnthropic: upstream},
				nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
			).WithManagedSubscriptions(leaser)
			svc.subscriptionModels.denyManaged(auth.SubscriptionOwner{APIKeyID: "key-1"}.PoolKey(), "opaque-claude", providers.ProviderAnthropic, "claude-opus-5", time.Now().Add(time.Minute))

			installationID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
			ctx := billing.WithSubscriptionOnly(managedSubscriptionContext(auth.SubscriptionProviderClaude), billing.SubscriptionOnlyCreditsDepleted)
			ctx = context.WithValue(ctx, InstallationIDContextKey{}, installationID.String())
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(tc.body)))

			err := tc.run(svc, ctx, tc.body, rec, req)

			require.Error(t, err)
			var upstreamErr *providers.UpstreamErrorResponse
			require.ErrorAs(t, err, &upstreamErr)
			assert.Equal(t, http.StatusNotFound, upstreamErr.Status)
			assert.Zero(t, upstream.calls, "a cached denial must not dispatch the rejected account")
			require.NotEmpty(t, store.upserts)
			expired := store.upserts[len(store.upserts)-1]
			assert.Equal(t, pinEvictionReasonSubscriptionModel, expired.Reason)
			assert.Empty(t, expired.Model)
			assert.True(t, expired.PinnedUntil.Before(time.Now()))
		})
	}
}
