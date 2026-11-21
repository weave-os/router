package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

// A /force-model turn whose Codex seats are all rate-limited past the short
// wait must not fail as an organization allowlist problem (400): Weave credits
// serve the forced model instead.
func TestSubscriptionStateForcedModelThrottledPoolFundsCredits(t *testing.T) {
	openAI := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{{writeBytes: []byte("paid answer")}}}
	svc := withHMMPorts(NewService(stateModelRouter{}, dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: openAI}), nil, nil, false, "", "", nil)).
		WithManagedSubscriptions(&stateModelLeaser{})
	seats := []*auth.SubscriptionAccount{activeCodexAccount("seat-1"), activeCodexAccount("seat-2")}
	ctx := WithManagedSubscriptionCandidates(WithManagedSubscriptionUsage(managedSubscriptionContext(auth.SubscriptionProviderCodex)), seats)
	for _, seat := range seats {
		require.True(t, svc.recordManagedSubscriptionFailure(ctx, providers.ProviderOpenAI, stateCodexModel, subscriptions.Lease{AccountID: seat.ID}, codexRateLimitedErr()))
	}
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{stateCodexModel})
	ctx = withTurnFailures(ctx)
	req := router.Request{Candidates: router.Candidates{AllowedModels: allowedModelsForRequest(ctx)}, EnabledProviders: modelSet([]string{providers.ProviderOpenAI})}
	rec := httptest.NewRecorder()
	buffer := newPreludeBuffer(rec)
	inputs := dispatch.FailoverInputs{
		Writer: rec, Prelude: buffer, SubscriptionStateRequest: &req,
		SubscriptionStateWinnerProvider: new(string),
		InitialDecision:                 router.Decision{Model: stateCodexModel, Provider: providers.ProviderOpenAI, Reason: translate.ReasonUserForceModel},
		Purpose:                         inference.PurposeOpenAIChatCompletions,
		BuildAlternative: func(router.Decision) (dispatch.UpstreamAttempt, error) {
			return func(attemptCtx context.Context, decision router.Decision, client dispatch.Upstream) error {
				buffer.Seal()
				return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequestWithContext(attemptCtx, http.MethodPost, "/v1/responses", nil))
			}, nil
		},
	}

	_, err := svc.dispatchWithFallback(ctx, inputs)

	require.NoError(t, err, "a throttled pool is not an allowlist problem and must not fail the turn")
	require.Equal(t, 1, openAI.calls, "the forced model is served once on Weave credits")
}
