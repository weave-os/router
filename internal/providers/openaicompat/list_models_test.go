package openaicompat_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"

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

// Cortex serves /api/v2/cortex/models next to /api/v2/cortex/v1/chat/completions,
// so a key whose base URL ends in /v1 must be retried one segment up.
func TestListModels_FallsBackAboveV1On404(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		if r.URL.Path != "/api/v2/cortex/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"models":["openai-gpt-5.2","claude-opus-5"]}`))
	}))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("gw-token", srv.URL+"/api/v2/cortex/v1")
	models, err := c.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-opus-5", "openai-gpt-5.2"}, models)
	assert.Equal(t, []string{"/api/v2/cortex/v1/models", "/api/v2/cortex/models"}, gotPaths)
}

func TestListModels_RetriesWithEntityWhenGatewayDemandsOne(t *testing.T) {
	var attempts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		attempts = append(attempts, string(body))
		if len(body) == 0 {
			http.Error(w, `{"code":"391910","message":"Invalid input value. null"}`, http.StatusUnsupportedMediaType)
			return
		}
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		_, _ = w.Write([]byte(`{"models":["claude-4-sonnet","openai-gpt-5"]}`))
	}))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("token", srv.URL)
	models, err := c.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-4-sonnet", "openai-gpt-5"}, models)
	assert.Equal(t, []string{"", "{}"}, attempts)
}
