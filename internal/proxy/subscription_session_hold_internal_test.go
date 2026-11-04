package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

// After a session's Codex pool is exhausted on provider 5xx and the included
// lane rescues it on Claude, a new user prompt shortly after must not walk the
// overloaded pool again: that is the GPT -> Claude -> GPT flip at every prompt.
func TestSubscriptionStatePoolOverloadHoldsSessionOffModel(t *testing.T) {
	overloaded := fakeOutcome{err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(`{"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"server_error"}}`)}}
	codex := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{overloaded, overloaded, overloaded}}
	claude := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("first answer")}, {writeBytes: []byte("second answer")}}}
	svc := NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderAnthropic: claude, providers.ProviderOpenAI: codex}, nil, false, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderClaude: true, subscriptions.ProviderCodex: true}})
	now := time.Date(2026, 11, 4, 6, 41, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	serve := func() router.Decision {
		ctx := context.WithValue(managedSubscriptionTestContext(), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}, auth.SubscriptionProviderCodex: {}})
		ctx = context.WithValue(ctx, InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
		ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{SessionID: "codex-session"})
		ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateCodexModel, stateClaudeModel})
		ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
		ctx = withTurnFailures(ctx)
		req := router.Request{AllowedModels: allowedModelsForRequest(ctx), EnabledProviders: modelSet([]string{providers.ProviderAnthropic, providers.ProviderOpenAI})}
		rec := httptest.NewRecorder()
		buffer := newPreludeBuffer(rec)
		var winner router.Decision
		_, err := svc.dispatchWithFallback(ctx, failoverInputs{
			w: rec, buf: buffer, subscriptionStateRequest: &req,
			subscriptionStateWinnerProvider: new(string),
			initialDecision:                 router.Decision{Model: stateCodexModel, Provider: providers.ProviderOpenAI},
			purpose:                         inference.PurposeAnthropicMessages,
			buildAlternative: func(router.Decision) (dispatchAttempt, error) {
				return func(attemptCtx context.Context, decision router.Decision, client providers.Client) error {
					buffer.Seal()
					return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				}, nil
			},
			onAlternative: func(decision router.Decision) { winner = decision },
		})
		require.NoError(t, err)
		return winner
	}

	require.Equal(t, stateClaudeModel, serve().Model)
	codexCalls := codex.calls
	require.Positive(t, codexCalls)

	now = now.Add(2 * time.Minute)
	require.Equal(t, stateClaudeModel, serve().Model)
	require.Equal(t, codexCalls, codex.calls, "the overloaded pool must stay out for the session's next prompts")

	now = now.Add(10 * time.Minute)
	codex.outcomes = append(codex.outcomes[:codex.calls], fakeOutcome{writeBytes: []byte("recovered")})
	require.Equal(t, stateCodexModel, serve().Model, "the hold expires so a recovered pool serves again")
}

func TestSessionHoldCoversSiblingModelsOnThePool(t *testing.T) {
	svc := &Service{now: time.Now}
	ctx := context.WithValue(WithManagedSubscriptionUsage(context.Background()), ClientIdentityContextKey{}, ClientIdentity{SessionID: "codex-session"})
	svc.markSubscriptionPoolUnavailable(ctx, subscriptions.ProviderCodex)
	svc.holdSessionOffSubscriptionPool(ctx, providers.ProviderOpenAI, stateCodexModel)

	sibling := "gpt-6-luna"
	_, known := catalog.ByID(sibling)
	require.True(t, known)
	require.Equal(t, []string{stateClaudeModel}, svc.withoutHeldSubscriptionModels(ctx, []string{stateCodexModel, sibling, stateClaudeModel}),
		"every model on the held pool stays out, not just the one that failed")
}
