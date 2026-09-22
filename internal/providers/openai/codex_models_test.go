package openai_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchCodexModelCatalog_UsesCallerOAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		assert.Equal(t, "0.155.1", r.URL.Query().Get("client_version"))
		assert.Equal(t, "Bearer subscription-token", r.Header.Get("Authorization"))
		assert.Equal(t, "account-123", r.Header.Get("ChatGPT-Account-ID"))
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-6-sol"}]}`))
	}))
	defer upstream.Close()
	client := openai.NewClient("deployment-key", "https://api.openai.com")
	client.SetCodexBaseURL(upstream.URL)
	ctx := requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{
		APIKey: []byte("subscription-token"), AccountID: []byte("account-123"), OAuth: true,
	})
	catalog, err := client.FetchCodexModelCatalog(ctx, "0.155.1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"models":[{"slug":"gpt-6-sol"}]}`, string(catalog))
}

func TestFetchCodexModelCatalog_RejectsUnpairedCredential(t *testing.T) {
	client := openai.NewClient("deployment-key", "https://api.openai.com")
	ctx := requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{
		APIKey: []byte("api-key"), OAuth: false,
	})
	_, err := client.FetchCodexModelCatalog(ctx, "0.155.1")
	require.ErrorContains(t, err, "requires ChatGPT OAuth")
}
