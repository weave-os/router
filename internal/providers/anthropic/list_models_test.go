package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"

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

func TestListModels_WalksAllPages(t *testing.T) {
	var afterIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after_id")
		afterIDs = append(afterIDs, after)
		switch after {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-a"},{"id":"claude-b"}],"has_more":true,"last_id":"claude-b"}`))
		case "claude-b":
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-c"}],"has_more":false,"last_id":"claude-c"}`))
		default:
			t.Errorf("unexpected after_id %q", after)
		}
	}))
	defer srv.Close()

	c := anthropic.NewClient("tok", srv.URL, anthropic.WithAuthScheme(anthropic.AuthBearer))
	models, err := c.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-a", "claude-b", "claude-c"}, models)
	assert.Equal(t, []string{"", "claude-b"}, afterIDs)
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

// Snowflake Cortex speaks the Messages API at {base}/v1/messages but hosts no
// /v1/models; its catalog lives at {base}/models in a bare-array shape.
func TestListModels_FallsBackToGatewayCatalogOn404(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"models":["claude-opus-5","claude-haiku-4-5"]}`))
	}))
	defer srv.Close()

	c := anthropic.NewClient("gw-token", srv.URL, anthropic.WithAuthScheme(anthropic.AuthBearer))
	models, err := c.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-haiku-4-5", "claude-opus-5"}, models)
	assert.Equal(t, []string{"/v1/models", "/models"}, gotPaths)
}

func TestListModels_RetriesGatewayCatalogWithEntity(t *testing.T) {
	var attempts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		attempts = append(attempts, string(body))
		if len(body) == 0 {
			http.Error(w, `{"code":"390400","message":"request entity required"}`, http.StatusBadRequest)
			return
		}
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		_, _ = w.Write([]byte(`{"models":["claude-4-sonnet"]}`))
	}))
	defer srv.Close()

	c := anthropic.NewClient("gw-token", srv.URL, anthropic.WithAuthScheme(anthropic.AuthBearer))
	models, err := c.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-4-sonnet"}, models)
	assert.Equal(t, []string{"", "{}"}, attempts)
}
