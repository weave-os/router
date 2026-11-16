package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"weave-os/router/internal/flags"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	randomrouter "weave-os/router/internal/router/random_router"

	"weave-os/router/internal/subscriptions"
)

type randomTestRouter struct{}

func (*randomTestRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{}, nil
}

func TestRandomBenchmarkIdentityAndFundingBoundary(t *testing.T) {
	config := randomrouter.Config{APIKeyID: "key", InstallationID: "installation", OrganizationID: "org", SubscriptionAccountID: "required-seat", PolicySHA256: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Hour)}
	svc := newServiceWithProviders(t, nil).WithRandomBenchmark(&config, &randomTestRouter{})
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, config.APIKeyID)
	ctx = context.WithValue(ctx, ExternalIDContextKey{}, config.OrganizationID)
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, config.InstallationID)
	ctx = requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{Target: string(policyregistry.TargetInternal)})
	ctx = WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: "required-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}, {ID: "other-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	strict, _, err := svc.BeginRandomBenchmark(ctx, config.PolicySHA256)
	require.NoError(t, err)
	require.True(t, paidFallbackForbidden(strict))
	require.True(t, requestcontext.IncludedSubscriptionOnlyFrom(strict))
	require.Equal(t, "required-seat", subscriptionOwnerFromContext(strict).RequiredAccountID)
	require.Len(t, strict.Value(managedSubscriptionCandidatesKey{}), 1)
	_, _, err = svc.BeginRandomBenchmark(context.WithValue(ctx, APIKeyIDContextKey{}, "another-key"), config.PolicySHA256)
	require.Error(t, err)
	_, _, err = svc.BeginRandomBenchmark(ctx, strings.Repeat("b", 64))
	require.Error(t, err)
}

func TestRandomBenchmarkFailureHasOneProviderAttempt(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: planClient{Client: &fakeClient{name: providers.ProviderOpenAI}}})
			config := randomrouter.Config{ExpiresAt: time.Now().Add(time.Hour), SubscriptionAccountID: "seat"}
			ctx := context.WithValue(context.Background(), randomBenchmarkKey{}, &randomBenchmark{config: config})
			ctx = WithManagedSubscriptionUsage(ctx)
			execution := &randomBenchmarkExecution{lease: subscriptions.Lease{AccountID: "seat", State: auth.SubscriptionAccountStateActive}, credentials: &Credentials{OAuth: true, Source: credSourceCodexSubscription, APIKey: []byte("synthetic"), AccountID: []byte("account")}}
			ctx = context.WithValue(ctx, randomBenchmarkExecutionKey{}, execution)
			svc.managedSubscriptions = &scriptedSubscriptionLeaser{}
			executor, err := dispatch.NewExecutor(svc.clients, dispatch.WithSleep(func(context.Context, time.Duration) error { t.Fatal("strict dispatch slept before retry"); return nil }))
			require.NoError(t, err)
			svc.executor = executor
			calls := 0
			recorder := httptest.NewRecorder()
			_, err = svc.dispatchWithFallback(ctx, failoverInputs{w: recorder, purpose: inference.PurposeOpenAIResponses, initialDecision: router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}, attempt: func(_ context.Context, _ router.Decision, _ providers.Client) error {
				calls++
				return &providers.UpstreamErrorResponse{Status: status, Body: []byte(`{"error":"failed"}`)}
			}})
			require.Error(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, 1, execution.providerCalls)
			_, err = svc.dispatchWithFallback(ctx, failoverInputs{w: recorder, purpose: inference.PurposeOpenAIResponses, initialDecision: router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}, attempt: func(context.Context, router.Decision, providers.Client) error { calls++; return nil }})
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestRandomBenchmarkRejectsExcludedCandidateBeforeSelection(t *testing.T) {
	svc := newServiceWithProviders(t, nil)
	ctx := context.WithValue(context.Background(), randomBenchmarkKey{}, &randomBenchmark{config: randomrouter.Config{SubscriptionAccountID: "seat"}, policy: &randomTestRouter{}})
	_, err := svc.routeRandomBenchmark(ctx, router.Request{ExcludedModels: map[string]struct{}{catalog.ModelGPT61Terra: {}}})
	require.True(t, errors.Is(err, randomrouter.ErrUnavailable))
}

func TestRandomBenchmarkUsesOnlyExplicitExperimentFlag(t *testing.T) {
	config := randomrouter.Config{APIKeyID: "00000000-0000-4000-8000-000000000001", InstallationID: "00000000-0000-4000-8000-000000000002", OrganizationID: "synthetic-org", SubscriptionAccountID: "00000000-0000-4000-8000-000000000003", KeySHA256: strings.Repeat("a", 64), PolicySHA256: randomrouter.PolicySHA256, ExpiresAt: time.Now().Add(time.Hour)}
	svc := newServiceWithProviders(t, nil).WithRandomBenchmark(nil, randomrouter.New(nil))
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, config.APIKeyID)
	ctx = context.WithValue(ctx, ExternalIDContextKey{}, config.OrganizationID)
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, config.InstallationID)
	ctx = requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{Target: string(policyregistry.TargetInternal)})
	ctx = WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: config.SubscriptionAccountID, Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}})
	_, _, err := svc.BeginRandomBenchmark(ctx, config.PolicySHA256)
	require.Error(t, err)
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	ctx = flags.WithOverrides(ctx, flags.Overrides{Strings: map[flags.Key]string{flags.KeyRandomBenchmarkConfig: string(encoded)}})
	_, frozen, err := svc.BeginRandomBenchmark(ctx, config.PolicySHA256)
	require.NoError(t, err)
	require.Equal(t, config.APIKeyID, frozen.APIKeyID)
	require.Equal(t, config.SubscriptionAccountID, frozen.SubscriptionAccountID)
}
