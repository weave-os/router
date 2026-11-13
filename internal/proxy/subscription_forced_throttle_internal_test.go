package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

// A /force-model turn whose Codex seats are all rate-limited must report the
// throttle (retryable 429), not an organization allowlist problem (400).
func TestSubscriptionStateForcedModelThrottledPoolKeeps429(t *testing.T) {
	openAI := &fakeClient{name: providers.ProviderOpenAI}
	svc := NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderOpenAI: openAI}, nil, nil, false, "", "", nil).
		WithManagedSubscriptions(&stateModelLeaser{})
	seats := []*auth.SubscriptionAccount{activeCodexAccount("seat-1"), activeCodexAccount("seat-2")}
	ctx := WithManagedSubscriptionCandidates(WithManagedSubscriptionUsage(managedSubscriptionContext(auth.SubscriptionProviderCodex)), seats)
	for _, seat := range seats {
		require.True(t, svc.recordManagedSubscriptionFailure(ctx, providers.ProviderOpenAI, stateCodexModel, subscriptions.Lease{AccountID: seat.ID}, codexRateLimitedErr()))
	}
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{stateCodexModel})
	ctx = withTurnFailures(ctx)
	req := router.Request{AllowedModels: allowedModelsForRequest(ctx), EnabledProviders: modelSet([]string{providers.ProviderOpenAI})}
	rec := httptest.NewRecorder()
	buffer := newPreludeBuffer(rec)
	inputs := failoverInputs{
		w: rec, buf: buffer, subscriptionStateRequest: &req,
		subscriptionStateWinnerProvider: new(string),
		initialDecision:                 router.Decision{Model: stateCodexModel, Provider: providers.ProviderOpenAI, Reason: translate.ReasonUserForceModel},
		purpose:                         inference.PurposeOpenAIChatCompletions,
		buildAlternative: func(router.Decision) (dispatchAttempt, error) {
			return func(attemptCtx context.Context, decision router.Decision, client providers.Client) error {
				buffer.Seal()
				return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			}, nil
		},
	}

	_, err := svc.dispatchWithFallback(ctx, inputs)

	require.NotErrorIs(t, err, router.ErrAllowlistEmptiesPool, "a throttled pool is not an allowlist problem")
	requireThrottledRefusal(t, err)
	require.Zero(t, openAI.calls, "a throttled pool must not reach paid capacity")
}
