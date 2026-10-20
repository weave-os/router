package proxy_test

import (
	"context"
	"fmt"
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
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	gatewayOpus   catalog.ModelID = "claude-opus-5-5"
	gatewaySonnet catalog.ModelID = "claude-sonnet-5-5"
)

// gatewayAllowlistCtx carries installation policy independently of gateway aliases.
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

func TestService_GatewayForcedPinOutsideAllowlistRejected(t *testing.T) {
	const body = `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"analyze usage"}]}`
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:    providers.ProviderAnthropicGateway,
		Model:       gatewaySonnet.String(),
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropicGateway, Model: "claude-opus-5-5", Reason: "cluster:v0.2",
	}}
	gateway := anthropicOKProvider()
	svc := proxy.NewService(fr,
		map[string]providers.Client{providers.ProviderAnthropicGateway: gateway},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	ctx := gatewayAllowlistCtx([]string{gatewayOpus.String(), catalog.ModelGPT6Luna}, gatewaySonnet.String(), gatewayOpus.String(), catalog.ModelGPT6Luna)
	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(ctx, []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")))

	require.ErrorIs(t, err, proxy.ErrForcedModelExcluded)
	assert.Contains(t, err.Error(), "allowed-model list")
	assert.Equal(t, 0, fr.routeCalls, "a forbidden force must fail instead of routing elsewhere")
	assert.Empty(t, rec.Header().Get(proxy.HeaderRouterModel))
	assert.Empty(t, gateway.proxyBodies, "an excluded force must fail before provider dispatch")
}

func TestService_GatewayForceCommandEnforcesAllowlist(t *testing.T) {
	for _, test := range []struct {
		name      string
		allowed   []string
		aliased   []string
		model     string
		rejection string
	}{
		{name: "outside allowlist", allowed: []string{gatewayOpus.String(), catalog.ModelGPT6Luna}, aliased: []string{gatewayOpus.String(), gatewaySonnet.String()}, model: gatewaySonnet.String(), rejection: "allowed-model list"},
		{name: "alias with effort outside allowlist", allowed: []string{gatewayOpus.String()}, aliased: []string{gatewaySonnet.String()}, model: "sonnet:high", rejection: "allowed-model list"},
		{name: "inside allowlist", allowed: []string{gatewaySonnet.String()}, aliased: []string{gatewaySonnet.String()}, model: gatewaySonnet.String()},
		{name: "unrestricted installation", aliased: []string{gatewaySonnet.String()}, model: gatewaySonnet.String()},
		{name: "allowlisted but unaliased", allowed: []string{gatewaySonnet.String()}, aliased: []string{gatewayOpus.String()}, model: gatewaySonnet.String(), rejection: "gateway keys"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}]}`, gatewayOpus, "/force-model "+test.model)
			store := newFakePinStore()
			fr := &fakeRouter{decision: router.Decision{
				Provider: providers.ProviderAnthropicGateway, Model: "claude-opus-5-5", Reason: "cluster:v0.2",
			}}
			svc := proxy.NewService(fr,
				map[string]providers.Client{providers.ProviderAnthropicGateway: anthropicOKProvider()},
				nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

			ctx := gatewayAllowlistCtx(test.allowed, test.aliased...)
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec,
				httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

			if test.rejection != "" {
				assert.Contains(t, rec.Body.String(), "force-model rejected")
				assert.Contains(t, rec.Body.String(), test.rejection)
				assert.Empty(t, store.upserts, "a rejected command must not persist a forced pin")
				assert.Equal(t, 0, fr.routeCalls)
				return
			}
			assert.Contains(t, rec.Body.String(), "force-model applied")
			waitForUpsert(t, store)
			require.NotEmpty(t, store.upserts)
			assert.Equal(t, gatewaySonnet.String(), store.upserts[0].Model)
			assert.Equal(t, providers.ProviderAnthropicGateway, store.upserts[0].Provider)
		})
	}
}

func TestService_GatewayForceHeaderOutsideAllowlistRejected(t *testing.T) {
	for _, ingress := range []struct {
		name  string
		path  string
		body  string
		serve func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{name: "anthropic", path: "/v1/messages", body: `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`, serve: (*proxy.Service).ProxyMessages},
		{name: "openai", path: "/v1/chat/completions", body: `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hello"}]}`, serve: (*proxy.Service).ProxyOpenAIChatCompletion},
	} {
		t.Run(ingress.name, func(t *testing.T) {
			store := newFakePinStore()
			fr := &fakeRouter{decision: router.Decision{
				Provider: providers.ProviderAnthropicGateway, Model: "claude-opus-5-5", Reason: "cluster:v0.2",
			}}
			gateway := anthropicOKProvider()
			svc := proxy.NewService(fr,
				map[string]providers.Client{providers.ProviderAnthropicGateway: gateway},
				nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

			ctx := gatewayAllowlistCtx([]string{gatewayOpus.String(), catalog.ModelGPT6Luna}, gatewayOpus.String(), gatewaySonnet.String(), catalog.ModelGPT6Luna)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, ingress.path, nil)
			req.Header.Set(proxy.ForceModelHeader, "sonnet:high")
			err := ingress.serve(svc, ctx, []byte(ingress.body), rec, req)

			require.ErrorIs(t, err, proxy.ErrForcedModelExcluded)
			classification, ok := proxy.ClassifyDispatchError(err)
			require.True(t, ok)
			assert.Equal(t, http.StatusBadRequest, classification.Status)
			assert.Contains(t, classification.Message, "allowed-model list")
			assert.Empty(t, store.upserts)
			assert.Equal(t, 0, fr.routeCalls)
			assert.Empty(t, rec.Header().Get(proxy.HeaderRouterModel))
			assert.Empty(t, gateway.proxyBodies, "an excluded force must fail before provider dispatch")
		})
	}
}
