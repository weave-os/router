package openaicompat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/providers/anthropic"
	"workweave/router/internal/providers/openaicompat"
	"workweave/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cortexInference serves Cortex's documented chat surfaces —
// /api/v2/cortex/v1/chat/completions and /api/v2/cortex/v1/messages — and 404s
// everything else, recording every path tried.
func cortexInference(served string, paths *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.URL.Path)
		if r.URL.Path != served {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion"}`))
	}
}

func chatRequest() (providers.PreparedRequest, *http.Request) {
	return providers.PreparedRequest{
			Body:    []byte(`{"model":"claude-sonnet-4-5","messages":[]}`),
			Headers: make(http.Header),
		},
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
}

// A gateway base URL stored without the version segment the OpenAI-spec surface
// is mounted under (Snowflake Cortex: .../api/v2/cortex, chat at
// .../api/v2/cortex/v1/chat/completions) must still reach chat, and must stop
// re-probing the unversioned path once it has.
func TestProxy_GatewayBaseURLMissingVersionSegment(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(cortexInference("/api/v2/cortex/v1/chat/completions", &paths))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("tok", srv.URL+"/api/v2/cortex")
	prep, clientReq := chatRequest()

	rec := httptest.NewRecorder()
	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-sonnet-4-5"}, prep, rec, clientReq))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"/api/v2/cortex/chat/completions", "/api/v2/cortex/v1/chat/completions"}, paths)

	paths = nil
	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-sonnet-4-5"}, prep, httptest.NewRecorder(), clientReq))
	assert.Equal(t, []string{"/api/v2/cortex/v1/chat/completions"}, paths)
}

// A 404 that isn't a misplaced version segment must surface as the upstream's
// own 404, not the probe's.
func TestProxy_GatewayVersionProbeKeepsOriginalNotFound(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(cortexInference("/nothing-serves-this", &paths))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("tok", srv.URL+"/api/v2/cortex")
	prep, clientReq := chatRequest()
	err := c.Proxy(context.Background(), router.Decision{Model: "claude-sonnet-4-5"}, prep, httptest.NewRecorder(), clientReq)

	require.Error(t, err)
	assert.True(t, providers.IsUpstreamModelNotFound(err))
	assert.Equal(t, []string{"/api/v2/cortex/chat/completions", "/api/v2/cortex/v1/chat/completions"}, paths)
}

// A versioned base URL is served as-is: no probe, so an OpenRouter/Fireworks
// style endpoint never sees a duplicated version segment.
func TestProxy_VersionedBaseURLIsNotProbed(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(cortexInference("/api/v2/cortex/v1/chat/completions", &paths))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("tok", srv.URL+"/api/v2/cortex/v1")
	prep, clientReq := chatRequest()
	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-sonnet-4-5"}, prep, httptest.NewRecorder(), clientReq))
	assert.Equal(t, []string{"/api/v2/cortex/v1/chat/completions"}, paths)
}

// The Messages surface is the mirror case: Cortex serves it at
// .../api/v2/cortex/v1/messages, so an admin who mirrors the OpenAI base URL
// (.../api/v2/cortex/v1) would otherwise POST to .../v1/v1/messages.
func TestAnthropicProxy_GatewayBaseURLWithDuplicateVersionSegment(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(cortexInference("/api/v2/cortex/v1/messages", &paths))
	defer srv.Close()

	c := anthropic.NewClient("tok", srv.URL+"/api/v2/cortex/v1", anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep, clientReq := chatRequest()
	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-sonnet-4-5"}, prep, httptest.NewRecorder(), clientReq))
	assert.Equal(t, []string{"/api/v2/cortex/v1/v1/messages", "/api/v2/cortex/v1/messages"}, paths)
}

// The base URL Cortex's own Anthropic-SDK quickstart uses reaches Messages on
// the first try.
func TestAnthropicProxy_GatewayBaseURLWithoutVersionSegment(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(cortexInference("/api/v2/cortex/v1/messages", &paths))
	defer srv.Close()

	c := anthropic.NewClient("tok", srv.URL+"/api/v2/cortex", anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep, clientReq := chatRequest()
	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-sonnet-4-5"}, prep, httptest.NewRecorder(), clientReq))
	assert.Equal(t, []string{"/api/v2/cortex/v1/messages"}, paths)
}
