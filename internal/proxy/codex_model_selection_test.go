package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexNativeModelSelection_ForcesModelAndEffort(t *testing.T) {
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`)
	}}
	routerStub := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "fresh"}}
	svc := proxy.NewService(routerStub, map[string]providers.Client{providers.ProviderOpenAI: provider}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
	ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
	body := []byte(`{"model":"gpt-6-sol","input":"review this change","reasoning":{"effort":"max"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set(proxy.CodexNativeModelPinHeader, "1")
	req.Header.Set("Authorization", "Bearer subscription-token")
	req.Header.Set("ChatGPT-Account-ID", "account-123")
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
	assert.Zero(t, routerStub.routeCalls, "native selection must bypass automatic scoring")
	assert.Equal(t, "gpt-6-sol", rec.Header().Get(proxy.HeaderRouterModel))
	require.Len(t, provider.proxyBodies, 1)
	require.Len(t, provider.proxyCreds, 1)
	require.NotNil(t, provider.proxyCreds[0])
	assert.True(t, provider.proxyCreds[0].OAuth)
	assert.Equal(t, []byte("account-123"), provider.proxyCreds[0].AccountID)
	assert.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0])
	assert.Equal(t, "gpt-6-sol", gjson.GetBytes(provider.proxyBodies[0], "model").String())
	assert.Equal(t, "max", gjson.GetBytes(provider.proxyBodies[0], "reasoning.effort").String())
}

func TestCodexNativeModelSelection_AutomaticAndLegacyStillScore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
		optIn bool
		want  string
	}{
		{name: "automatic choice", model: proxy.CodexAutomaticModel, optIn: true, want: proxy.CodexAutomaticModel},
		{name: "older install", model: "gpt-6-sol", optIn: false, want: "gpt-6-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`)
			}}
			routerStub := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "fresh"}}
			svc := proxy.NewService(routerStub, map[string]providers.Client{providers.ProviderOpenAI: provider}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
			body := []byte(`{"model":"` + tc.model + `","input":"review this change","reasoning":{"effort":"max"}}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tc.optIn {
				req.Header.Set(proxy.CodexNativeModelPinHeader, "1")
			}
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
			assert.Positive(t, routerStub.routeCalls, "automatic routing must still invoke the scorer")
			require.NotNil(t, routerStub.capturedReq)
			assert.Equal(t, tc.want, routerStub.capturedReq.RequestedModel)
			assert.Equal(t, "gpt-5.6-luna", rec.Header().Get(proxy.HeaderRouterModel))
		})
	}
}
