package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"workweave/router/internal/providers/anthropic"
	"workweave/router/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModels_GatewayBearerAuth(t *testing.T) {
	var gotPath, gotAuth, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("anthropic-version")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-fable-5"},{"id":"claude-haiku-4-5"}]}`))
	}))
	defer srv.Close()

	c := anthropic.NewClient("gw-token", srv.URL, anthropic.WithAuthScheme(anthropic.AuthBearer))
	models, err := c.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-fable-5", "claude-haiku-4-5"}, models)
	assert.Equal(t, "/v1/models", gotPath)
	assert.Equal(t, "Bearer gw-token", gotAuth)
	assert.NotEmpty(t, gotVersion)
}

func TestListModels_BYOKCredentialsOverrideBaseURL(t *testing.T) {
	var gotAuth string
	byokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"cortex-claude"}]}`))
	}))
	defer byokSrv.Close()

	c := anthropic.NewClient("", "", anthropic.WithAuthScheme(anthropic.AuthBearer))
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey:  []byte("byok-token"),
		BaseURL: byokSrv.URL,
	})
	models, err := c.ListModels(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"cortex-claude"}, models)
	assert.Equal(t, "Bearer byok-token", gotAuth)
}

func TestListModels_UpstreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	c := anthropic.NewClient("tok", srv.URL, anthropic.WithAuthScheme(anthropic.AuthBearer))
	_, err := c.ListModels(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}
