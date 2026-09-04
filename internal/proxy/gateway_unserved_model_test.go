package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude Code's sidebar-title call: no tools, title json_schema output config.
const titleGenBody = `{"model":"claude-haiku-4-5","max_tokens":32,"messages":[{"role":"user","content":"hello"}],` +
	`"output_config":{"format":{"type":"json_schema","schema":{"properties":{"title":{"type":"string"}}}}}}`

const (
	unservedAlias = "grok-4.6"
	servedAlias   = "claude-haiku-4-5"
)

// aliasGateway answers 404 for one aliased model (the endpoint declares it but
// does not publish it) and 200 for everything else.
type aliasGateway struct {
	unserved   string
	servedWith []string
}

func (g *aliasGateway) Proxy(_ context.Context, decision router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	if decision.Model == g.unserved {
		return &providers.UpstreamErrorResponse{
			Status: http.StatusNotFound,
			Body:   []byte(`{"error":{"message":"unknown model: ` + decision.Model + `","type":"not_found"}}`),
		}
	}
	g.servedWith = append(g.servedWith, decision.Model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	return nil
}

func (g *aliasGateway) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

// ctxWithGatewayAliases attaches an openai_gateway BYOK key aliasing models.
func ctxWithGatewayAliases(installationID string, models ...string) context.Context {
	aliases := make(map[string]string, len(models))
	for _, m := range models {
		aliases[m] = m
	}
	key := &auth.ExternalAPIKey{
		InstallationID: installationID,
		Provider:       providers.ProviderOpenAIGateway,
		Plaintext:      []byte("gw-token"),
		BaseURL:        "https://cortex.example.com/api/v2/cortex",
		ModelAliases:   aliases,
	}
	return context.WithValue(authedCtx(installationID), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{key})
}

// Prod 2026-08-28: a gateway key aliased a model the endpoint does not serve;
// the 404 must teach the router to skip it on later turns.
func TestService_HardPin_TitleGen_GatewayModelNotFound_ExcludedOnLaterTurns(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "claude-opus-4-7", Reason: "cluster"}}

	// Mimics cluster.FastestModelForRequest: the cheapest aliased candidate
	// wins unless the request's exclusions rule it out.
	var seen []proxy.HardPinRequest
	resolver := func(req proxy.HardPinRequest) (string, string, bool) {
		seen = append(seen, req)
		for _, model := range []string{unservedAlias, servedAlias} {
			if _, excluded := req.ExcludedModels[model]; excluded {
				continue
			}
			for _, provider := range req.CustomBindings[model] {
				if _, isGateway := req.GatewayProviders[provider]; isGateway {
					return provider, model, true
				}
			}
		}
		return "", "", false
	}

	gateway := &aliasGateway{unserved: unservedAlias}
	svc := proxy.NewService(
		fr, map[string]providers.Client{providers.ProviderOpenAIGateway: gateway},
		nil, false, nil, store, false,
		providers.ProviderAnthropic, servedAlias,
		nil,
	).WithByokOnly(true).WithHardPinResolver(resolver)

	ctx := ctxWithGatewayAliases(uuid.New().String(), unservedAlias, servedAlias)

	firstErr := svc.ProxyMessages(ctx, []byte(titleGenBody), httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")))
	require.Error(t, firstErr, "the first turn still pays the endpoint's 404")

	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(titleGenBody), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	require.Len(t, seen, 2)
	assert.NotContains(t, seen[0].ExcludedModels, unservedAlias,
		"nothing is known about the alias before the first dispatch")
	assert.Contains(t, seen[1].ExcludedModels, unservedAlias,
		"a 404'd gateway alias must be excluded from later hard-pin resolution")
	assert.NotContains(t, seen[1].ExcludedModels, servedAlias,
		"only the model the endpoint refused is excluded")
	assert.Equal(t, []string{servedAlias}, gateway.servedWith)
	assert.Equal(t, servedAlias, rec.Header().Get(proxy.HeaderRouterModel))
}

// The memo is per (endpoint, model): an alias the gateway does serve must stay
// routable, or one bad alias would sideline the whole key.
func TestService_HardPin_TitleGen_GatewayModelNotFound_KeepsServedAliasesRoutable(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "claude-opus-4-7", Reason: "cluster"}}

	var seen []proxy.HardPinRequest
	resolver := func(req proxy.HardPinRequest) (string, string, bool) {
		seen = append(seen, req)
		if _, excluded := req.ExcludedModels[servedAlias]; excluded {
			return "", "", false
		}
		return providers.ProviderOpenAIGateway, servedAlias, true
	}

	gateway := &aliasGateway{unserved: unservedAlias}
	svc := proxy.NewService(
		fr, map[string]providers.Client{providers.ProviderOpenAIGateway: gateway},
		nil, false, nil, store, false,
		providers.ProviderAnthropic, servedAlias,
		nil,
	).WithByokOnly(true).WithHardPinResolver(resolver)

	ctx := ctxWithGatewayAliases(uuid.New().String(), unservedAlias, servedAlias)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyMessages(ctx, []byte(titleGenBody), rec,
			httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
		assert.Equal(t, servedAlias, rec.Header().Get(proxy.HeaderRouterModel))
	}
	require.Len(t, seen, 2)
	assert.NotContains(t, seen[1].ExcludedModels, servedAlias,
		"a model the endpoint served must never be memoized as unserved")
}

// A direct vendor's 404 says nothing about a gateway endpoint, and the vendor
// has its own binding walk — it must not feed the gateway memo.
func TestService_HardPin_TitleGen_VendorModelNotFound_DoesNotExcludeModel(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "claude-opus-4-7", Reason: "cluster"}}

	var seen []proxy.HardPinRequest
	resolver := func(req proxy.HardPinRequest) (string, string, bool) {
		seen = append(seen, req)
		return providers.ProviderAnthropic, servedAlias, true
	}

	vendor := &aliasGateway{unserved: servedAlias}
	svc := proxy.NewService(
		fr, map[string]providers.Client{providers.ProviderAnthropic: vendor},
		nil, false, nil, store, false,
		providers.ProviderAnthropic, servedAlias,
		nil,
	).WithHardPinResolver(resolver).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	ctx := authedCtx(uuid.New().String())
	for i := 0; i < 2; i++ {
		require.Error(t, svc.ProxyMessages(ctx, []byte(titleGenBody), httptest.NewRecorder(),
			httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
	}
	require.Len(t, seen, 2)
	assert.NotContains(t, seen[1].ExcludedModels, servedAlias)
}
