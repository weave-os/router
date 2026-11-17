package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
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
	return randomrouter.Config{SchemaVersion: "random_router_named.v2", InstallationID: "00000000-0000-4000-8000-000000000002", OrganizationID: "synthetic-org", PolicySHA256: randomrouter.PolicySHA256, ExpiresAt: time.Now().Add(time.Hour)}
}
func randomTestContext(config randomrouter.Config) context.Context {
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "00000000-0000-4000-8000-000000000001")
	ctx = context.WithValue(ctx, ExternalIDContextKey{}, config.OrganizationID)
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, config.InstallationID)
	ctx = requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{Target: string(policyregistry.TargetInternal)})
	return WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{{ID: "00000000-0000-4000-8000-000000000003", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}, {ID: "other-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}})
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
	strict, _, err := BeginRandomBenchmark(svc, ctx, config.PolicySHA256)
	require.NoError(t, err)
	require.Equal(t, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000001")
	require.True(t, paidFallbackForbidden(strict))
	require.True(t, requestcontext.IncludedSubscriptionOnlyFrom(strict))
	require.Empty(t, subscriptionOwnerFromContext(strict).ExcludedAccountIDs)
	_, _, err = BeginRandomBenchmark(svc, context.WithValue(ctx, APIKeyIDContextKey{}, "another-key"), config.PolicySHA256)
	require.NoError(t, err)
	_, _, err = BeginRandomBenchmark(svc, ctx, strings.Repeat("b", 64))
	require.Error(t, err)
}
func TestRandomHostedRetriesKeepSelectedModel(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{
		{AccountID: "seat-one", AccessToken: "synthetic", ProviderAccount: "provider-one", State: auth.SubscriptionAccountStateActive},
		{AccountID: "seat-two", AccessToken: "synthetic", ProviderAccount: "provider-two", State: auth.SubscriptionAccountStateActive},
	}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenAI: &fakeClient{name: providers.ProviderOpenAI}}).WithManagedSubscriptions(leaser)
	state := &randomrouter.RequestState{Config: randomrouter.Config{ExpiresAt: time.Now().Add(time.Hour)}, Prepared: true, SelectedModel: catalog.ModelGPT6Luna}
	ctx := randomrouter.WithRequest(managedSubscriptionContext(auth.SubscriptionProviderCodex), state)
	ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
	ctx = context.WithValue(ctx, subscriptionOnlyAttemptKey{}, true)
	calls := 0
	inputs := failoverInputs{w: httptest.NewRecorder(), purpose: inference.PurposeOpenAIResponses, initialDecision: router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}, attempt: func(ctx context.Context, d router.Decision, _ providers.Client) error {
		calls++
		require.Equal(t, catalog.ModelGPT6Luna, d.Model)
		require.True(t, CredentialsFromContext(ctx).OAuth)
		if calls == 1 {
			return &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`{"error":"overloaded"}`)}
		}
		return nil
	}}
	_, err := svc.dispatchWithFallback(ctx, inputs)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	inputs.initialDecision.Model = catalog.ModelGPT61Sol.String()
	_, err = svc.dispatchWithFallback(ctx, inputs)
	require.ErrorIs(t, err, randomrouter.ErrUnavailable)
	require.Equal(t, 2, calls)
}
func TestRandomBenchmarkRequiresAuthorizedContext(t *testing.T) {
	_, err := randomrouter.New(func(int) (int, error) { t.Fatal("unauthorized draw"); return 0, nil }).Route(context.Background(), router.Request{})
	require.ErrorIs(t, err, randomrouter.ErrUnavailable)
}

func TestRandomResponsesNeverRepairOrRescue(t *testing.T) {
	scenarios := []struct {
		name    string
		outcome fakeOutcome
		success bool
	}{
		{"endpoint_rejection", fakeOutcome{err: &providers.UpstreamErrorResponse{Status: 404, Body: []byte(`{"error":{"message":"Responses endpoint is not supported"}}`)}}, false},
		{"rate_limit", fakeOutcome{err: &providers.UpstreamErrorResponse{Status: 429, Body: []byte(`{"error":{"message":"rate limit"}}`)}}, false},
		{"overload", fakeOutcome{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`{"error":{"message":"overloaded"}}`)}}, false},
		{"partial_stream", fakeOutcome{writeBytes: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"), err: io.ErrUnexpectedEOF}, false},
		{"failed_stream", fakeOutcome{writeBytes: []byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"synthetic failure\"}}}\n\n")}, false},
		{"refusal", fakeOutcome{writeBytes: []byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null,\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"synthetic refusal\"}]}]}}\n\n")}, false},
		{"incomplete", fakeOutcome{writeBytes: []byte("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n")}, false},
		{"completed", fakeOutcome{writeBytes: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"completed\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null,\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"completed\"}]}]}}\n\n")}, true},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			config := randomTestConfig()
			client := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{scenario.outcome}}
			svc := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, catalog.ModelGPT61Sol.String(), nil).
				WithManagedSubscriptions(&scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "00000000-0000-4000-8000-000000000003", AccessToken: "synthetic", ProviderAccount: "synthetic-account", State: auth.SubscriptionAccountStateActive}}, repeatLast: true}).
				WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyRandomBenchmark, Router: randomrouter.New(func(int) (int, error) { return 0, nil }), Unavailable: randomrouter.ErrUnavailable})
			encoded, err := json.Marshal(config)
			require.NoError(t, err)
			ctx := flags.WithOverrides(randomTestContext(config), flags.Overrides{Strings: map[flags.Key]string{flags.KeyRandomBenchmarkConfig: string(encoded)}})
			ctx, _, err = BeginRandomBenchmark(svc, ctx, config.PolicySHA256)
			require.NoError(t, err)
			ctx = WithManagedSubscriptionUsage(ctx)
			state := randomrouter.RequestFrom(ctx)
			state.Prepared = true
			state.Lease = subscriptions.Lease{AccountID: "00000000-0000-4000-8000-000000000003", State: auth.SubscriptionAccountStateActive}
			state.Credentials = &Credentials{OAuth: true, Source: credSourceCodexSubscription, APIKey: []byte("synthetic"), AccountID: []byte("synthetic-account")}
			body := []byte(`{"model":"weave-random","stream":true,"input":[{"role":"user","content":"synthetic benchmark turn"}]}`)
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, randomrouter.ResponsesPath, bytes.NewReader(body))
			req.Header.Set("Session-Id", "synthetic-session")
			recorder := httptest.NewRecorder()
			err = svc.ProxyOpenAIResponses(ctx, body, recorder, req)
			if scenario.success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, 1, client.calls)
			require.Equal(t, 1, state.ProviderCalls)
		})
	}
}

func TestRandomInputRejectsNonstreamBeforeDispatch(t *testing.T) {
	ctx := randomrouter.WithRequest(context.Background(), &randomrouter.RequestState{})
	headers := http.Header{"Session-Id": []string{"synthetic"}}
	require.ErrorIs(t, ValidateRandomBenchmarkInput(ctx, []byte(`{"model":"weave-random","stream":false,"input":"synthetic"}`), headers), randomrouter.ErrUnavailable)
	require.NoError(t, ValidateRandomBenchmarkInput(ctx, []byte(`{"model":"weave-random","stream":true,"input":"synthetic"}`), headers))
}

func TestRandomPolicyUnavailableRefusesClientRetry(t *testing.T) {
	classification, matched := ClassifyDispatchError(randomrouter.ErrUnavailable)
	require.True(t, matched)
	require.Equal(t, DispatchErrorPolicyUnavailable, classification.Kind)
	require.Equal(t, http.StatusServiceUnavailable, classification.Status)
	require.True(t, classification.NoClientRetry)
	require.False(t, classification.RetryAfter)
}

func TestRandomUndispatchedFailureRetainsFundingIdentity(t *testing.T) {
	config := randomTestConfig()
	state := &randomrouter.RequestState{Config: config, Prepared: true, Lease: subscriptions.Lease{AccountID: "00000000-0000-4000-8000-000000000003"}, Credentials: &Credentials{Source: credSourceCodexSubscription}}
	ctx := randomrouter.WithRequest(randomTestContext(config), state)
	telemetry := InsertTelemetryParams{}
	applySubscriberTelemetry(ctx, &telemetry)
	require.Empty(t, telemetry.SubscriptionAccountID)
	require.Empty(t, telemetry.CredentialSource)
	require.Empty(t, telemetry.CapacitySource)
	require.False(t, servedOnSubscription(ctx))
	require.Equal(t, "skipped", telemetry.PlannerOutcome)
}

func TestRandomSummarizerFailsBeforeResolution(t *testing.T) {
	s := &ProviderSummarizer{}
	ctx := randomrouter.WithRequest(context.Background(), &randomrouter.RequestState{})
	_, err := s.resolve(ctx, policy.ResolutionRequest{Purpose: policy.PurposeHandoverSummary})
	require.ErrorIs(t, err, randomrouter.ErrUnavailable)
}

func TestRandomRejectsRoutingControlsBeforeDraw(t *testing.T) {
	for _, header := range []string{ForceModelHeader, "X-Weave-Policy-Pin", "X-Weave-Force-Cluster", "X-Weave-Model-Passthrough", "X-Weave-Router-Strategy", "X-Weave-Force-Effort", ClassifierThreadHeader, "X-Weave-Codex-Native-Model-Pin"} {
		h := http.Header{"Session-Id": []string{"synthetic-session"}}
		h.Set(header, "synthetic-control")
		ctx := randomrouter.WithRequest(context.Background(), &randomrouter.RequestState{})
		require.Error(t, ValidateRandomBenchmarkInput(ctx, []byte(`{"model":"weave-random","stream":true,"input":"synthetic"}`), h), header)
	}
}

type randomAttemptStore struct {
	TelemetryRepository
	rows []InsertInferenceAttemptParams
}

func (s *randomAttemptStore) InsertInferenceAttempt(_ context.Context, row InsertInferenceAttemptParams) error {
	s.rows = append(s.rows, row)
	return nil
}

func TestRandomActualAttemptsRecordIndependentFundingAndUsage(t *testing.T) {
	store := &randomAttemptStore{}
	svc := newServiceWithProviders(t, nil)
	svc.telemetry = store
	config := randomTestConfig()
	state := &randomrouter.RequestState{Config: config, Prepared: true, SelectedModel: catalog.ModelGPT6Luna}
	ctx := randomrouter.WithRequest(context.Background(), state)
	ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{OAuth: true, Source: credSourceCodexSubscription, APIKey: []byte("synthetic"), AccountID: []byte("synthetic-account")})
	decision := router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}
	plans, err := svc.inferencePlans()
	require.NoError(t, err)
	plan, err := plans.ResolveRouted(policy.RoutedResolutionRequest{Purpose: policy.PurposeOpenAIResponses, Decision: decision, Bindings: []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}})
	require.NoError(t, err)
	for _, seat := range []string{"first-seat", "second-seat"} {
		ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{OAuth: true, Source: credSourceCodexSubscription, APIKey: []byte("synthetic"), AccountID: []byte("synthetic-account"), SubscriptionAccountID: seat})
		client := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{{writeBytes: []byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n")}}}
		inputs := failoverInputs{initialDecision: decision, attempt: func(ctx context.Context, d router.Decision, p providers.Client) error {
			return p.Proxy(ctx, d, providers.PreparedRequest{Endpoint: providers.EndpointResponses}, httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/responses", nil))
		}}
		_, err := invokeLeasedProviderAttempt(svc, ctx, plan, leasedProviderAttempt{inputs: inputs, decision: decision, client: client, managed: true, target: plan.SelectedTarget(), lease: subscriptions.Lease{AccountID: seat}})
		require.NoError(t, err)
	}
	require.Len(t, store.rows, 2)
	require.Equal(t, 2, state.ProviderCalls)
	for i, row := range store.rows {
		require.Equal(t, []string{"first-seat", "second-seat"}[i], row.Event.Funding.SubscriptionAccountID)
		require.Equal(t, config.InstallationID, row.Event.Funding.PoolID)
		require.Equal(t, inference.FundingSourceIncludedCodex, row.Event.Funding.Source)
		require.Equal(t, catalog.ModelGPT6Luna, row.Event.Target.CatalogID)
		require.True(t, row.Event.Usage.Known)
		require.Equal(t, 10, row.Event.Usage.InputTokens)
		require.Equal(t, 2, row.Event.Usage.OutputTokens)
	}
}
