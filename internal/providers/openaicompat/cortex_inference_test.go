package openaicompat_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cortexInference serves only served, 404s everything else, recording paths tried.
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

// A base URL missing the version segment must still reach chat and must memoize
// the working path so the second call skips the probe.
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

// A real error on the versioned path must win over the unversioned probe 404,
// and the working path must be memoized so the next call skips the probe.
func TestProxy_GatewayVersionProbeSurfacesRealError(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/api/v2/cortex/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid request parameters: Function tools with reasoning_effort are not supported"}`))
	}))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("tok", srv.URL+"/api/v2/cortex")
	prep, clientReq := chatRequest()
	err := c.Proxy(context.Background(), router.Decision{Model: "gpt-5.6-luna"}, prep, httptest.NewRecorder(), clientReq)

	require.Error(t, err)
	var upstream *providers.UpstreamErrorResponse
	require.True(t, errors.As(err, &upstream))
	assert.Equal(t, http.StatusBadRequest, upstream.Status)
	assert.Contains(t, string(upstream.Body), "reasoning_effort")
	assert.Equal(t, []string{"/api/v2/cortex/chat/completions", "/api/v2/cortex/v1/chat/completions"}, paths)

	paths = nil
	_ = c.Proxy(context.Background(), router.Decision{Model: "gpt-5.6-luna"}, prep, httptest.NewRecorder(), clientReq)
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

// A versioned base URL paired with a versioned suffix retries without the duplicate.
func TestAnthropicProxy_GatewayBaseURLWithDuplicateVersionSegment(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(cortexInference("/api/v2/cortex/v1/messages", &paths))
	defer srv.Close()

	c := anthropic.NewClient("tok", srv.URL+"/api/v2/cortex/v1", anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep, clientReq := chatRequest()
	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-sonnet-4-5"}, prep, httptest.NewRecorder(), clientReq))
	assert.Equal(t, []string{"/api/v2/cortex/v1/v1/messages", "/api/v2/cortex/v1/messages"}, paths)
}

// The Anthropic surface must surface the retried path's real error too, rather
// than masking it with the duplicate-version probe's 404, and memoize the path.
func TestAnthropicProxy_GatewayVersionProbeSurfacesRealError(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/api/v2/cortex/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"unknown model \"claude-opus-9\""}`))
	}))
	defer srv.Close()

	c := anthropic.NewClient("tok", srv.URL+"/api/v2/cortex/v1", anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep, clientReq := chatRequest()
	err := c.Proxy(context.Background(), router.Decision{Model: "claude-opus-9"}, prep, httptest.NewRecorder(), clientReq)

	require.Error(t, err)
	var upstream *providers.UpstreamErrorResponse
	require.True(t, errors.As(err, &upstream))
	assert.Equal(t, http.StatusBadRequest, upstream.Status)
	assert.Contains(t, string(upstream.Body), "unknown model")

	paths = nil
	_ = c.Proxy(context.Background(), router.Decision{Model: "claude-opus-9"}, prep, httptest.NewRecorder(), clientReq)
	assert.Equal(t, []string{"/api/v2/cortex/v1/messages"}, paths)
}

// An unversioned base URL reaches Messages on the first try.
func TestAnthropicProxy_GatewayBaseURLWithoutVersionSegment(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(cortexInference("/api/v2/cortex/v1/messages", &paths))
	defer srv.Close()

	c := anthropic.NewClient("tok", srv.URL+"/api/v2/cortex", anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep, clientReq := chatRequest()
	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-sonnet-4-5"}, prep, httptest.NewRecorder(), clientReq))
	assert.Equal(t, []string{"/api/v2/cortex/v1/messages"}, paths)
}

// A Responses-endpoint request must go to /responses, not chat/completions,
// while keeping the version-probe fallback.
func TestProxy_ResponsesEndpointUsesResponsesPath(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(cortexInference("/api/v2/cortex/v1/responses", &paths))
	defer srv.Close()

	c := openaicompat.NewGatewayClient("tok", srv.URL+"/api/v2/cortex")
	prep, clientReq := chatRequest()
	prep.Endpoint = providers.EndpointResponses

	rec := httptest.NewRecorder()
	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "gpt-5.6-luna"}, prep, rec, clientReq))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"/api/v2/cortex/responses", "/api/v2/cortex/v1/responses"}, paths)
}
