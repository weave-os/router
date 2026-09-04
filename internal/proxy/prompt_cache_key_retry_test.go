package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

// pckStrictGatewayProvider rejects any body carrying prompt_cache_key
// with an unknown-field 400; everything else succeeds.
type pckStrictGatewayProvider struct {
	fakeProvider
}

func (p *pckStrictGatewayProvider) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	saved := make([]byte, len(prep.Body))
	copy(saved, prep.Body)
	p.proxyBodies = append(p.proxyBodies, saved)
	if strings.Contains(string(prep.Body), `"prompt_cache_key"`) {
		return &providers.UpstreamErrorResponse{
			Status: http.StatusBadRequest,
			Body:   []byte(`{"error":{"message":"prompt_cache_key: extra inputs are not permitted"}}`),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	return nil
}

// TestService_ProxyOpenAIChatCompletion_GatewayPromptCacheKeyRejectionRetriesAndMemoizes
// verifies the strip-and-retry fires once on the first strict-gateway 400, then memoizes.
func TestService_ProxyOpenAIChatCompletion_GatewayPromptCacheKeyRejectionRetriesAndMemoizes(t *testing.T) {
	gw := &pckStrictGatewayProvider{}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAIGateway,
		Model:    "grok-4.6",
		Reason:   "hmm:test",
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderOpenAIGateway: gw,
	}, nil, false, nil, nil, false, providers.ProviderOpenAIGateway, "grok-4.6", nil)

	ctx := context.WithValue(context.Background(), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderOpenAIGateway, Plaintext: []byte("gw-key"), BaseURL: "https://gw.example.com/v1"},
	})
	body := []byte(`{"model":"grok-4.6","messages":[{"role":"system","content":"You are a terse assistant."},{"role":"user","content":"hello"}]}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, body, rec, req))

	require.Len(t, gw.proxyBodies, 2, "strict 400 must trigger exactly one strip-and-retry")
	assert.Contains(t, string(gw.proxyBodies[0]), `"prompt_cache_key"`)
	assert.NotContains(t, string(gw.proxyBodies[1]), `"prompt_cache_key"`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"content":"ok"`)

	// Later turn against the memoized endpoint: single dispatch, no hint.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, body, rec2, req2))
	require.Len(t, gw.proxyBodies, 3, "memoized endpoint must not pay the 400 again")
	assert.NotContains(t, string(gw.proxyBodies[2]), `"prompt_cache_key"`)
	assert.Equal(t, http.StatusOK, rec2.Code)
}
