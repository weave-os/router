package openaicompat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"workweave/router/internal/providers/openaicompat"
	"workweave/router/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModels_ReturnsSortedDedupedIDs(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"llama3.1-70b"},{"id":"claude-fable-5"},{"id":"llama3.1-70b"},{"id":""}]}`))
	}))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("deploy-token", srv.URL)
	models, err := c.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-fable-5", "llama3.1-70b"}, models)
	assert.Equal(t, "/models", gotPath)
	assert.Equal(t, "Bearer deploy-token", gotAuth)
}

func TestListModels_BYOKCredentialsOverrideBaseURLAndKey(t *testing.T) {
	var gotAuth string
	byokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"cortex-model"}]}`))
	}))
	defer byokSrv.Close()

	c := openaicompat.NewGatewayClient("deploy-token", "http://127.0.0.1:1/unreachable")
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey:  []byte("byok-token"),
		BaseURL: byokSrv.URL,
	})
	models, err := c.ListModels(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"cortex-model"}, models)
	assert.Equal(t, "Bearer byok-token", gotAuth)
}

func TestListModels_UpstreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("bad-token", srv.URL)
	_, err := c.ListModels(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestListModels_NoBaseURL(t *testing.T) {
	c := openaicompat.NewGatewayClient("token", "")
	_, err := c.ListModels(context.Background())
	require.Error(t, err)
}

func TestListModels_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":"not-a-list"}`))
	}))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("token", srv.URL)
	_, err := c.ListModels(context.Background())
	require.Error(t, err)
}
