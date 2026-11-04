package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const compactionStrategyModel = "gpt-6.1-terra"

func TestCodexCompactionAfterAutoReview(t *testing.T) {
	version, err := cluster.ResolveVersion(cluster.LatestVersion)
	require.NoError(t, err)
	bundle, err := cluster.LoadBundle(version)
	require.NoError(t, err)
	store := newForceModelMapStore()
	svc := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: nil}, nil, false, nil, store, false,
		providers.ProviderAnthropic, policy.UtilityHardPinDefaultModel, nil).
		WithCompactionHardPin(true).
		WithAvailableModels(map[string]struct{}{compactionStrategyModel: {}}).
		WithHardPinResolver(func(req HardPinRequest) (string, string, bool) {
			return cluster.FastestModelForRequest(bundle.Metadata, bundle.Registry, req.EnabledProviders, req.ExcludedModels, nil,
				cluster.RequestBindings{Custom: req.CustomBindings, Gateways: req.GatewayProviders})
		})
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex, SessionID: "compaction-strategy-test"})
	ctx = router.WithStrategy(ctx, router.StrategyHMM)
	env, err := translate.ParseOpenAI([]byte(`{"model":"weave-hmm-20261019-0001v1","messages":[{"role":"user","content":"You are performing a CONTEXT CHECKPOINT COMPACTION. Summarize the work."}]}`))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	sessionKey := deriveSessionKeyForRequest(ctx, env, "test-key")
	installationID := uuid.New()

	// Both requests serve Terra, but codex-auto-review uses the deployment's
	// default classifier instead of the conversation's explicit routing model.
	for _, strategy := range []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding} {
		turnCtx := router.WithStrategy(ctx, strategy)
		decision := router.Decision{Provider: providers.ProviderOpenAI, Model: compactionStrategyModel,
			Metadata: &router.RoutingMetadata{Strategy: string(strategy)}}
		svc.writeNewPin(turnCtx, installationID, sessionKey, sessionpin.DefaultRole, decision)
		svc.recordTurnUsage(turnCtx, turnLoopResult{
			InstallationID: installationID, SessionKey: sessionKey, PinRole: sessionpin.DefaultRole,
			Strategy: strategy, Decision: decision,
		}, decision.Provider, decision.Model, 100, 10, 0, 0, false)
	}
	history, found, err := store.Get(ctx, sessionKey, hmmHistoryRole(sessionpin.DefaultRole))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, router.StrategyHMMEmbedding, history.Strategy)
	require.Equal(t, compactionStrategyModel, history.LastServedModel)

	// OAuth-only eligibility excludes the legacy bundle's OpenAI models.
	legacyExcluded := map[string]struct{}{}
	for _, entry := range bundle.Registry.DeployedModels {
		if entry.Provider == providers.ProviderOpenAI {
			legacyExcluded[entry.Model] = struct{}{}
		}
	}
	turn, err := svc.runTurnLoop(ctx, env, features, "test-key", installationID, "", nil, router.Request{
		RequestedModel:   string(router.ModelHMM),
		EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
		ExcludedModels:   legacyExcluded,
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat: router.WireFormatOpenAI, Endpoint: router.EndpointOpenAIResponses, NativeOnly: true,
		},
	})
	if err != nil {
		class, _ := ClassifyDispatchError(err)
		t.Logf("compaction rejected: HTTP %d: %s", class.Status, class.Message)
	}
	require.NoError(t, err)
	assert.Equal(t, turntype.Compaction, turn.TurnType)
	assert.True(t, turn.HardPinned)
	assert.Equal(t, providers.ProviderOpenAI, turn.Decision.Provider)
	assert.Equal(t, compactionStrategyModel, turn.Decision.Model)
	assert.Equal(t, policy.OverrideSourceSession, turn.Origin)

	_, active := svc.loadPin(ctx, sessionKey, sessionpin.DefaultRole)
	assert.False(t, active, "ordinary routing must still reject another strategy's pin")
	assert.Empty(t, svc.loadHMMHistory(ctx, sessionKey, sessionpin.DefaultRole).LastServedModel)
}

func TestCodexCompactionDispatchAcrossStrategies(t *testing.T) {
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {
			Provider: providers.ProviderOpenAI, LastServedModel: compactionStrategyModel,
			Strategy: router.StrategyHMMEmbedding, LastTurnEndedAt: time.Now(),
		},
	}}
	svc := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: untypedResponsesStreamProvider{}},
		nil, false, nil, store, false, providers.ProviderAnthropic, policy.UtilityHardPinDefaultModel, nil).
		WithCompactionHardPin(true).
		WithHardPinResolver(func(HardPinRequest) (string, string, bool) { return "", "", false })
	ctx := router.WithStrategy(context.Background(), router.StrategyHMM)
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	body := []byte(`{"model":"weave-hmm-20261019-0001v1","stream":true,"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"Review the patch."}]},
		{"type":"agent_message","author":"child","recipient":"parent","content":[{"type":"encrypted_content","encrypted_content":"synthetic-opaque-content"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary."}]}]}`)
	rec := httptest.NewRecorder()
	err := svc.ProxyOpenAIResponses(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, compactionStrategyModel, rec.Header().Get(HeaderRouterModel))
	assert.Contains(t, rec.Body.String(), `"delta":"handoff"`)
	assert.Contains(t, rec.Body.String(), "event: response.completed")
}

func TestCompactionCrossStrategyHistoryHonorsEligibility(t *testing.T) {
	for _, test := range []struct {
		name      string
		req       router.Request
		available map[string]struct{}
	}{
		{name: "provider disabled", req: router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}},
		{name: "model excluded", req: router.Request{ExcludedModels: map[string]struct{}{compactionStrategyModel: {}}}},
		{name: "model automatically disabled", req: router.Request{AutomaticExcludedModels: map[string]struct{}{compactionStrategyModel: {}}}},
		{name: "model unavailable", available: map[string]struct{}{policy.PrecompactionDefaultModel: {}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &rolePinStore{byRole: map[string]sessionpin.Pin{
				hmmHistoryRole(sessionpin.DefaultRole): {
					Provider: providers.ProviderOpenAI, LastServedModel: compactionStrategyModel,
					Strategy: router.StrategyHMMEmbedding, LastTurnEndedAt: time.Now(),
				},
			}}
			svc := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: nil, providers.ProviderAnthropic: nil},
				nil, false, nil, store, false, providers.ProviderAnthropic, policy.UtilityHardPinDefaultModel, nil).
				WithAvailableModels(test.available)
			test.req.ClientApp = ClientAppCodex
			ctx := router.WithStrategy(context.Background(), router.StrategyHMM)
			_, _, ok := svc.compactionSessionModel(ctx, [sessionpin.SessionKeyLen]byte{}, sessionpin.DefaultRole, test.req)
			assert.False(t, ok)
		})
	}
}
