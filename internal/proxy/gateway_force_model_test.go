package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatewayAllowlistCtx is a gateway-exclusive installation whose allowlist
// narrows automatic routing to one of the models its gateway key aliases.
func gatewayAllowlistCtx(allowed []string, aliased ...string) context.Context {
	aliases := make(map[string]string, len(aliased))
	for _, m := range aliased {
		aliases[m] = m
	}
	id := uuid.New().String()
	key := &auth.ExternalAPIKey{
		InstallationID: id,
		Provider:       providers.ProviderAnthropicGateway,
		Plaintext:      []byte("gw-token"),
		BaseURL:        "https://gateway.example.com",
		ModelAliases:   aliases,
	}
	ctx := context.WithValue(authedCtx(id), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{key})
	return context.WithValue(ctx, proxy.InstallationAllowedModelsContextKey{}, allowed)
}

func anthropicOKProvider() *fakeProvider {
	return &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	}}
}

// The tenant's gateway is the access boundary, so its allowlist shapes only
// what the router picks; an explicit force reaches any model the gateway aliases.
func TestService_GatewayForcedPinOutsideAllowlistServes(t *testing.T) {
	const body = `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"analyze usage"}]}`
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:    providers.ProviderAnthropicGateway,
		Model:       "claude-opus-5",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropicGateway, Model: "claude-opus-5-5", Reason: "cluster:v0.2",
	}}
	svc := proxy.NewService(fr,
		map[string]providers.Client{providers.ProviderAnthropicGateway: anthropicOKProvider()},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := gatewayAllowlistCtx([]string{"claude-opus-5-5"}, "claude-opus-5", "claude-opus-5-5")
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.Equal(t, 0, fr.routeCalls, "the force must not fall through to automatic routing")
	assert.Equal(t, "claude-opus-5", rec.Header().Get(proxy.HeaderRouterModel))
	assert.NotContains(t, rec.Body.String(), "could not be served")
}

func TestService_GatewayForceCommandOutsideAllowlistApplies(t *testing.T) {
	const body = `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"/force-model claude-opus-5"}]}`
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropicGateway, Model: "claude-opus-5-5", Reason: "cluster:v0.2",
	}}
	svc := proxy.NewService(fr,
		map[string]providers.Client{providers.ProviderAnthropicGateway: anthropicOKProvider()},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := gatewayAllowlistCtx([]string{"claude-opus-5-5"}, "claude-opus-5", "claude-opus-5-5")
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.NotContains(t, rec.Body.String(), "force-model rejected")
	waitForUpsert(t, store)
	require.NotEmpty(t, store.upserts)
	assert.Equal(t, "claude-opus-5", store.upserts[0].Model)
	assert.Equal(t, providers.ProviderAnthropicGateway, store.upserts[0].Provider)
}

// The gateway bypass covers only the positive allowlist: a model the gateway
// does not alias is still refused.
func TestService_GatewayForceCommandUnaliasedModelStillRejected(t *testing.T) {
	const body = `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"/force-model claude-sonnet-5-5"}]}`
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropicGateway, Model: "claude-opus-5-5", Reason: "cluster:v0.2",
	}}
	svc := proxy.NewService(fr,
		map[string]providers.Client{providers.ProviderAnthropicGateway: anthropicOKProvider()},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := gatewayAllowlistCtx([]string{"claude-opus-5-5"}, "claude-opus-5", "claude-opus-5-5")
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.Contains(t, rec.Body.String(), "force-model rejected")
	assert.Contains(t, rec.Body.String(), "gateway keys")
	assert.Empty(t, store.upserts)
}
