package proxy

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	randomrouter "weave-os/router/internal/router/random_router"
	"weave-os/router/internal/subscriptions"
)

func randomTestConfig() randomrouter.Config {
	return randomrouter.Config{APIKeyID: "00000000-0000-4000-8000-000000000001", InstallationID: "00000000-0000-4000-8000-000000000002", OrganizationID: "synthetic-org", SubscriptionAccountID: "00000000-0000-4000-8000-000000000003", KeySHA256: strings.Repeat("a", 64), PolicySHA256: randomrouter.PolicySHA256, ExpiresAt: time.Now().Add(time.Hour)}
}
func randomTestContext(config randomrouter.Config) context.Context {
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, config.APIKeyID)
	ctx = context.WithValue(ctx, ExternalIDContextKey{}, config.OrganizationID)
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, config.InstallationID)
	ctx = requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{Target: string(policyregistry.TargetInternal)})
	return WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: config.SubscriptionAccountID, Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}, {ID: "other-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}})
}
func TestRandomBenchmarkIdentityAndFundingBoundary(t *testing.T) {
	config := randomTestConfig()
	svc := newServiceWithProviders(t, nil).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyRandomBenchmark, Router: randomrouter.New(nil), Unavailable: randomrouter.ErrUnavailable})
	ctx := randomTestContext(config)
	_, _, err := BeginRandomBenchmark(svc, ctx, config.PolicySHA256)
	require.Error(t, err)
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	ctx = flags.WithOverrides(ctx, flags.Overrides{Strings: map[flags.Key]string{flags.KeyRandomBenchmarkConfig: string(encoded)}})
	strict, frozen, err := BeginRandomBenchmark(svc, ctx, config.PolicySHA256)
	require.NoError(t, err)
	require.Equal(t, config.APIKeyID, frozen.APIKeyID)
	require.True(t, paidFallbackForbidden(strict))
	require.True(t, requestcontext.IncludedSubscriptionOnlyFrom(strict))
	require.Equal(t, config.SubscriptionAccountID, subscriptionOwnerFromContext(strict).RequiredAccountID)
	require.Len(t, strict.Value(managedSubscriptionCandidatesKey{}), 1)
	_, _, err = BeginRandomBenchmark(svc, context.WithValue(ctx, APIKeyIDContextKey{}, "another-key"), config.PolicySHA256)
	require.Error(t, err)
	_, _, err = BeginRandomBenchmark(svc, ctx, strings.Repeat("b", 64))
	require.Error(t, err)
}
func TestRandomBenchmarkFailureHasOneProviderAttempt(t *testing.T) {
	for _, status := range []int{400, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: &fakeClient{name: providers.ProviderOpenAI}}).WithManagedSubscriptions(&scriptedSubscriptionLeaser{})
			state := &randomrouter.RequestState{Config: randomrouter.Config{ExpiresAt: time.Now().Add(time.Hour), SubscriptionAccountID: "seat"}, Lease: subscriptions.Lease{AccountID: "seat", State: auth.SubscriptionAccountStateActive}, Credentials: &Credentials{OAuth: true, Source: credSourceCodexSubscription, APIKey: []byte("synthetic"), AccountID: []byte("account")}, Prepared: true}
			ctx := randomrouter.WithRequest(WithManagedSubscriptionUsage(context.Background()), state)
			executor, err := dispatch.NewExecutor(svc.clients, dispatch.WithSleep(func(context.Context, time.Duration) error { t.Fatal("strict dispatch retried"); return nil }))
			require.NoError(t, err)
			svc.executor = executor
			calls := 0
			recorder := httptest.NewRecorder()
			inputs := failoverInputs{w: recorder, purpose: inference.PurposeOpenAIResponses, initialDecision: router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}, attempt: func(context.Context, router.Decision, providers.Client) error {
				calls++
				return &providers.UpstreamErrorResponse{Status: status, Body: []byte(`{"error":"synthetic"}`)}
			}}
			_, err = svc.dispatchWithFallback(ctx, inputs)
			require.Error(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, 1, state.ProviderCalls)
			_, err = svc.dispatchWithFallback(ctx, inputs)
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
}
func TestRandomBenchmarkRequiresAuthorizedContext(t *testing.T) {
	_, err := randomrouter.New(func(int) (int, error) { t.Fatal("unauthorized draw"); return 0, nil }).Route(context.Background(), router.Request{})
	require.ErrorIs(t, err, randomrouter.ErrUnavailable)
}
