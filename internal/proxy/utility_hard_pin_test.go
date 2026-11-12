package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

type utilityHardPinSetup struct {
	name      string
	clients   []string
	byokOnly  bool
	ctx       func(installationID string) context.Context
	wantModel string
	wantVia   string
}

func gatewayKeyCtx(gateway string, aliased ...string) func(string) context.Context {
	return func(installationID string) context.Context {
		key := &auth.ExternalAPIKey{
			InstallationID: installationID,
			Provider:       gateway,
			Plaintext:      []byte("gw-token"),
			BaseURL:        "https://gateway.example.com/v1",
			ModelAliases:   map[string]string{},
		}
		for _, model := range aliased {
			key.ModelAliases[model] = "gw-" + model
		}
		return context.WithValue(authedCtx(installationID), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{key})
	}
}

func newUtilityHardPinSvc(setup utilityHardPinSetup, fr *fakeRouter) *proxy.Service {
	clients := make(map[string]providers.Client, len(setup.clients))
	for _, name := range setup.clients {
		clients[name] = &fakeProvider{}
	}
	if _, ok := clients[providers.ProviderOpenAIGateway]; ok {
		clients[providers.ProviderOpenAIGateway] = &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}}
	}
	return proxy.NewService(
		fr, clients, nil, false, nil, newFakePinStore(), true,
		policy.UtilityHardPinDefaultProvider, policy.UtilityHardPinDefaultModel,
		nil,
	).WithByokOnly(setup.byokOnly).WithHardPinResolver(proxy.CatalogUtilityHardPin)
}

// Explore sub-agent hard pins resolve from the catalog against each
// installation's reachable providers, exclusions and gateway aliases.
func TestService_UtilityHardPin_CatalogTargetPerInstallation(t *testing.T) {
	setups := []utilityHardPinSetup{
		{
			name:     "byok_only",
			clients:  []string{providers.ProviderAnthropic, providers.ProviderOpenAI},
			byokOnly: true,
			ctx: func(id string) context.Context {
				return authedCtxWithExternalKey(id, providers.ProviderOpenAI, []byte("sk-byok"))
			},
			wantModel: catalog.ModelIDGPT41Nano.String(),
			wantVia:   providers.ProviderOpenAI,
		},
		{
			name:    "excluded_default_model",
			clients: []string{providers.ProviderAnthropic},
			ctx: func(id string) context.Context {
				return context.WithValue(authedCtx(id), proxy.InstallationExcludedModelsContextKey{}, []string{policy.UtilityHardPinDefaultModel})
			},
			wantModel: catalog.ModelIDClaudeHaiku55.String(),
			wantVia:   providers.ProviderAnthropic,
		},
		{
			name:      "custom_binding",
			clients:   []string{providers.ProviderOpenAIGateway},
			byokOnly:  true,
			ctx:       gatewayKeyCtx(providers.ProviderOpenAIGateway, catalog.ModelIDGPT41Mini.String()),
			wantModel: catalog.ModelIDGPT41Mini.String(),
			wantVia:   providers.ProviderOpenAIGateway,
		},
		{
			name:      "gateway_only",
			clients:   []string{providers.ProviderAnthropic, providers.ProviderAnthropicGateway},
			ctx:       gatewayKeyCtx(providers.ProviderAnthropicGateway, catalog.ModelIDClaudeSonnet46.String(), policy.UtilityHardPinDefaultModel),
			wantModel: policy.UtilityHardPinDefaultModel,
			wantVia:   providers.ProviderAnthropicGateway,
		},
	}
	for _, setup := range setups {
		t.Run(setup.name, func(t *testing.T) {
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus47.String()}}
			svc := newUtilityHardPinSvc(setup, fr)
			rec := httptest.NewRecorder()
			httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

			require.NoError(t, svc.ProxyMessages(legacyExploreCtx(setup.ctx(uuid.New().String())), []byte(exploreBody), rec, httpReq))

			assert.Equal(t, 0, fr.routeCalls, "utility hard pins bypass the scorer")
			assert.Equal(t, setup.wantModel, rec.Header().Get(proxy.HeaderRouterModel))
			assert.Equal(t, setup.wantVia, rec.Header().Get(proxy.HeaderRouterProvider))
		})
	}
}

// No servable utility model is a fixable configuration problem, not a
// retryable router outage.
func TestService_UtilityHardPin_NoEligibleProviderIs400(t *testing.T) {
	setup := utilityHardPinSetup{
		clients:  []string{providers.ProviderAnthropic},
		byokOnly: true,
		ctx:      authedCtx,
	}
	svc := newUtilityHardPinSvc(setup, &fakeRouter{})
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	err := svc.ProxyMessages(legacyExploreCtx(setup.ctx(uuid.New().String())), []byte(exploreBody), rec, httpReq)

	require.ErrorIs(t, err, router.ErrNoEligibleProvider)
	assert.NotErrorIs(t, err, router.ErrRoutingUnavailable)
	class, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorNoEligibleProvider, class.Kind)
	assert.Equal(t, http.StatusBadRequest, class.Status)
}

// legacyExploreCtx selects a non-HMM strategy, the only kind on which the
// legacy hardPinExplore default still hard-pins sub-agent turns.
func legacyExploreCtx(ctx context.Context) context.Context {
	return router.WithStrategy(ctx, router.StrategyRL)
}
