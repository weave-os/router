package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionHoldRequiresAnUnavailablePoolMark(t *testing.T) {
	svc := &Service{hmmServing: hmmPorts(), clock: time.Now}
	ctx := context.WithValue(WithManagedSubscriptionUsage(context.Background()), ClientIdentityContextKey{}, ClientIdentity{SessionID: "codex-session"})

	svc.holdSessionOffSubscriptionPool(ctx, providers.ProviderOpenAI, stateCodexModel)

	assert.Equal(t, []string{stateCodexModel}, svc.withoutHeldSubscriptionModels(ctx, []string{stateCodexModel}),
		"a failure the pool's own capacity mark does not confirm must not hold the session off it")
}

func TestForcedClaudeClientAppRefusalIgnoresTurnsOnTheCallersOwnCredential(t *testing.T) {
	usage := &ManagedSubscriptionUsage{ClientAppRefused: true}
	ctx := context.WithValue(context.Background(), credentials.ManagedSubscriptionUsageContextKey{}, usage)
	forced := router.Decision{Reason: translate.ReasonUserForceModel}

	assert.True(t, forcedClaudeClientAppRefused(ctx, forced, nil))
	assert.False(t, forcedClaudeClientAppRefused(ctx, forced, &Credentials{OAuth: true}), "the caller's own OAuth login is not standing in for refusing seats")
	assert.False(t, forcedClaudeClientAppRefused(ctx, forced, &Credentials{Source: credSourceBYOK}), "a customer key is not standing in for refusing seats")
	assert.False(t, forcedClaudeClientAppRefused(ctx, router.Decision{Reason: "cluster"}, nil), "routed turns need pool-wide funding authorization instead")
}

func TestCustomerFundingCountsOnlyTheCustomersOwnCredentials(t *testing.T) {
	key := []byte("synthetic-key")
	assert.True(t, customerFunded(&Credentials{APIKey: key, Source: credSourceBYOK}))
	assert.True(t, customerFunded(&Credentials{APIKey: key, Source: credSourceClient}), "a key the client sent with the request is the customer's own")
	assert.True(t, customerFunded(&Credentials{APIKey: key, OAuth: true}))
	assert.False(t, customerFunded(&Credentials{APIKey: key}), "a deployment key is Weave-funded")
	assert.False(t, customerFunded(&Credentials{Source: credSourceBYOK}), "a credential without key material funds nothing")
}

func TestRejectedOwnCodexLoginOutranksALaterRateLimit(t *testing.T) {
	rejected := &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized}
	limited := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}

	assert.Same(t, rejected, includedFailureAfter(rejected, limited, true), "the caller must sign in again before a rate limit matters")
	assert.Same(t, limited, includedFailureAfter(nil, limited, false))
	assert.Same(t, rejected, includedFailureAfter(limited, rejected, true))
	assert.Same(t, limited, includedFailureAfter(limited, rejected, false), "a rejected pool login is not the caller's own")
}

func TestEnterpriseCapRescueNeedsAUsableAnthropicKey(t *testing.T) {
	refused := context.WithValue(context.Background(), credentials.ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{ClaudeEnterpriseCapRefused: true})

	assert.False(t, claudeEnterpriseCapRescue(refused), "with no customer key the cap has nothing to rescue onto")
	keyed := context.WithValue(refused, credentials.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok"), FundingPriority: auth.FundingPriorityPlanFirst},
	})
	assert.True(t, claudeEnterpriseCapRescue(keyed))
}

func TestUnleasableSeatsDoNotHoldPaidFallbackEvenWhileEnabled(t *testing.T) {
	now := time.Now()
	svc := &Service{hmmServing: hmmPorts(), clock: func() time.Time { return now }}
	reconnect := activeCodexAccount("seat-reconnect")
	reconnect.State = auth.SubscriptionAccountStateReconnectRequired
	disabled := activeCodexAccount("seat-disabled")
	disabled.State = auth.SubscriptionAccountStateDisabled

	assert.NoError(t, svc.authorizeSubscriptionAPI(throttleTestContext(reconnect, disabled), subscriptions.ProviderCodex, inference.Target{}, "", "", clientAppRefusalFunding{}),
		"a seat that can never be leased can never prove exhaustion either")
}

func heldCodexOnlyService(t *testing.T) (*fakeClient, func() router.Decision) {
	t.Helper()
	codex := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{{writeBytes: []byte("answer")}}}
	svc := withHMMPorts(NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderOpenAI: codex}, nil, nil, false, "", "", nil)).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderCodex: true}})
	svc.clock = time.Now
	turnCtx := func() context.Context {
		ctx := context.WithValue(managedSubscriptionTestContext(), credentials.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderCodex: {}})
		ctx = context.WithValue(ctx, requestcontext.InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
		ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{SessionID: "codex-session"})
		ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateCodexModel})
		ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
		return withTurnFailures(ctx)
	}
	held := turnCtx()
	svc.markSubscriptionPoolUnavailable(held, subscriptions.ProviderCodex)
	svc.holdSessionOffSubscriptionPool(held, providers.ProviderOpenAI, stateCodexModel)
	require.Empty(t, svc.withoutHeldSubscriptionModels(held, []string{stateCodexModel}))

	serve := func() router.Decision {
		ctx := turnCtx()
		req := router.Request{Candidates: router.Candidates{AllowedModels: allowedModelsForRequest(ctx)}, EnabledProviders: modelSet([]string{providers.ProviderOpenAI})}
		rec := httptest.NewRecorder()
		buffer := newPreludeBuffer(rec)
		var winner router.Decision
		_, err := svc.dispatchWithFallback(ctx, dispatch.FailoverInputs{
			Writer: rec, Prelude: buffer, SubscriptionStateRequest: &req,
			SubscriptionStateWinnerProvider: new(string),
			InitialDecision:                 router.Decision{Model: stateCodexModel, Provider: providers.ProviderOpenAI},
			Purpose:                         inference.PurposeAnthropicMessages,
			BuildAlternative: func(router.Decision) (dispatch.UpstreamAttempt, error) {
				return func(attemptCtx context.Context, decision router.Decision, client dispatch.Upstream) error {
					buffer.Seal()
					return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequestWithContext(attemptCtx, http.MethodPost, "/v1/messages", nil))
				}, nil
			},
			OnAlternative: func(decision router.Decision) { winner = decision },
		})
		require.NoError(t, err)
		return winner
	}
	return codex, serve
}

func TestHeldPoolIsTriedWhenNothingElseTheRequestAdmitsRemains(t *testing.T) {
	codex, serve := heldCodexOnlyService(t)

	winner := serve()

	assert.Equal(t, stateCodexModel, winner.Model)
	assert.Equal(t, 1, codex.calls, "a held pool is the last resort, not a reason to leave the included lane untried")
}

func TestMemberPickNoSubscriptionServesSkipsTheIncludedLane(t *testing.T) {
	codex := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{{writeBytes: []byte("covered swap")}}}
	paid := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("member pick")}}}
	svc := withHMMPorts(NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderOpenAI: codex, providers.ProviderOpenRouter: paid}, nil, nil, false, "", "", nil)).
		WithManagedSubscriptions(&stateModelLeaser{activeProviders: map[subscriptions.Provider]bool{subscriptions.ProviderCodex: true}}).
		WithDeploymentKeyedProviders(modelSet([]string{providers.ProviderOpenRouter}))
	ctx := flags.WithOverrides(managedSubscriptionTestContext(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyPlanUserClusterLists: true}})
	ctx = context.WithValue(ctx, auth.UserClusterModelListsContextKey{}, map[string][]string{"member-cluster": {statePaidModel}})
	ctx = context.WithValue(ctx, credentials.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderCodex: {}})
	ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateCodexModel})
	ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	ctx = withTurnFailures(ctx)
	req := router.Request{Candidates: router.Candidates{AllowedModels: allowedModelsForRequest(ctx)}, EnabledProviders: modelSet([]string{providers.ProviderOpenAI, providers.ProviderOpenRouter})}
	rec := httptest.NewRecorder()
	buffer := newPreludeBuffer(rec)
	var winner router.Decision
	_, err := svc.dispatchWithFallback(ctx, dispatch.FailoverInputs{
		Writer: rec, Prelude: buffer, SubscriptionStateRequest: &req,
		SubscriptionStateWinnerProvider: new(string),
		InitialDecision:                 router.Decision{Model: statePaidModel, Provider: providers.ProviderOpenRouter},
		Purpose:                         inference.PurposeAnthropicMessages,
		BuildAlternative: func(router.Decision) (dispatch.UpstreamAttempt, error) {
			return func(attemptCtx context.Context, decision router.Decision, client dispatch.Upstream) error {
				buffer.Seal()
				return client.Proxy(attemptCtx, decision, providers.PreparedRequest{}, buffer, httptest.NewRequestWithContext(attemptCtx, http.MethodPost, "/v1/messages", nil))
			}, nil
		},
		OnAlternative: func(decision router.Decision) { winner = decision },
	})

	require.NoError(t, err)
	assert.Equal(t, statePaidModel, winner.Model)
	assert.Zero(t, codex.calls, "the member's own pick is served on paid capacity, not swapped for a covered model")
}

func TestCoveringSubscriptionMustMatchTheRouteFamily(t *testing.T) {
	codexBearer := http.Header{}
	codexBearer.Set("Authorization", "Bearer eyJhbGciOi.codex.jwt")
	codexBearer.Set("ChatGPT-Account-ID", "acct-abc-123")

	assert.True(t, RequestPresentsCoveringSubscription(context.Background(), codexBearer, routePathResponses))
	assert.False(t, RequestPresentsCoveringSubscription(context.Background(), codexBearer, routePathMessages),
		"a Codex login cannot serve Messages, so it must not exempt a prepaid turn there")
}

func TestPlanForbiddenPaidFallbackKeepsTheCustomerKeyOut(t *testing.T) {
	ctx := context.WithValue(context.Background(), credentials.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
	ctx = context.WithValue(ctx, credentials.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderOpenRouter, Plaintext: []byte("synthetic-byok")}})
	enabled := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenRouter: {}}

	assert.Equal(t, enabled, restrictToFundedProviders(ctx, http.Header{}, enabled))
	attemptOnly := context.WithValue(ctx, subscriptionOnlyAttemptKey{}, true)
	assert.Equal(t, map[string]struct{}{providers.ProviderAnthropic: {}}, restrictToFundedProviders(attemptOnly, http.Header{}, enabled),
		"a turn the plan keeps on the subscription may not reach the customer's paid key")
}

func TestClearedCredentialsOverrideTheWinningSubscription(t *testing.T) {
	usage := &ManagedSubscriptionUsage{Finished: true, WinningCredentials: &Credentials{APIKey: []byte("sk-ant-oat01-winning"), OAuth: true}}
	ctx := context.WithValue(context.Background(), credentials.ManagedSubscriptionUsageContextKey{}, usage)
	require.Same(t, usage.WinningCredentials, CredentialsFromContext(ctx))

	assert.Nil(t, CredentialsFromContext(clearCredentials(ctx)), "a synthetic call must not borrow the turn's subscription credential")
}

func TestKeyFirstPreferenceIgnoresAnEmptyKeyRow(t *testing.T) {
	keyFirst := &auth.ExternalAPIKey{Provider: providers.ProviderOpenAI, Plaintext: []byte("synthetic-byok"), FundingPriority: auth.FundingPriorityKeyFirst}
	emptyPlanFirst := &auth.ExternalAPIKey{Provider: providers.ProviderOpenAI, FundingPriority: auth.FundingPriorityPlanFirst}
	ctx := context.WithValue(context.Background(), credentials.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{keyFirst, emptyPlanFirst})

	assert.True(t, byokFirst(ctx, providers.ProviderOpenAI), "a row with no key material is not the key that serves")
}

func TestExternalKeyPrincipalChangesWithTheUpstreamAccount(t *testing.T) {
	key := func(account, user string) *auth.ExternalAPIKey {
		return &auth.ExternalAPIKey{ID: "key-1", Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok"), AuthAccount: account, AuthUser: user}
	}
	principal := func(k *auth.ExternalAPIKey) string {
		return credentials.BuildCredentialsMap([]*auth.ExternalAPIKey{k})[providers.ProviderAnthropic].PrincipalID
	}

	assert.NotEqual(t, principal(key("account-a", "user")), principal(key("account-b", "user")))
	assert.NotEqual(t, principal(key("account-a", "user-a")), principal(key("account-a", "user-b")))
}
