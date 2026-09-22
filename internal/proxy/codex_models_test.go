package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexModelCatalog_UsesCallerOAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		assert.Equal(t, "0.155.1", r.URL.Query().Get("client_version"))
		assert.Equal(t, "Bearer subscription-token", r.Header.Get("Authorization"))
		assert.Equal(t, "account-123", r.Header.Get("ChatGPT-Account-ID"))
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-6-sol","visibility":"list","supported_reasoning_levels":[{"effort":"max"}]}]}`))
	}))
	defer upstream.Close()
	client := openai.NewClient("deployment-key", "https://api.openai.com")
	client.SetCodexBaseURL(upstream.URL)
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer subscription-token")
	headers.Set("ChatGPT-Account-ID", "account-123")
	catalog, err := svc.CodexModelCatalog(context.Background(), headers, "0.155.1")
	require.NoError(t, err)
	assert.Equal(t, "gpt-6-sol", gjson.GetBytes(catalog, "models.0.slug").String())
	assert.Equal(t, proxy.CodexAutomaticModel, gjson.GetBytes(catalog, "models.1.slug").String())
	assert.Equal(t, "max", gjson.GetBytes(catalog, "models.1.supported_reasoning_levels.0.effort").String())
}

func TestCodexModelCatalog_RequiresPairedOAuth(t *testing.T) {
	client := openai.NewClient("deployment-key", "https://api.openai.com")
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer subscription-token")
	_, err := svc.CodexModelCatalog(context.Background(), headers, "0.155.1")
	require.ErrorContains(t, err, "requires ChatGPT OAuth and account ID")
}
