package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const preservedMessagesBody = `{"model":"claude-opus-4-7","max_tokens":64,"messages":[{"role":"user","content":"hi"}],` +
	`"thinking":{"type":"enabled","budget_tokens":1024},"metadata":{"user_id":"caller"},"unknown_native_field":{"kept":true}}`

func TestProxy_PreserveNativeKeepsOriginalModelAndFields(t *testing.T) {
	var (
		gotPath   string
		gotAPIKey string
		gotBody   []byte
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_native"}`))
	}))
	defer upstream.Close()

	c := anthropic.NewClient("deployment-key", upstream.URL).WithModelIDMap(map[string]string{"claude-opus-4-7": "vendor-opus"})
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: []byte(preservedMessagesBody), Headers: make(http.Header), PreserveNative: true}

	err := c.Proxy(context.Background(), router.Decision{Model: "claude-opus-4-7", Provider: providers.ProviderAnthropic}, prep, rec, clientReq)
	require.NoError(t, err)

	assert.Equal(t, "/v1/messages", gotPath)
	assert.Equal(t, "deployment-key", gotAPIKey, "credential shaping is unchanged under PreserveNative")
	assert.JSONEq(t, preservedMessagesBody, string(gotBody), "the original body, model spelling and native-only fields must reach Anthropic untouched")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"id":"msg_native"`)
}

func TestProxy_ModelIDMapStillAppliesWithoutPreserveNative(t *testing.T) {
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_mapped"}`))
	}))
	defer upstream.Close()

	c := anthropic.NewClient("deployment-key", upstream.URL).WithModelIDMap(map[string]string{"claude-opus-4-7": "vendor-opus"})
	prep := providers.PreparedRequest{Body: []byte(preservedMessagesBody), Headers: make(http.Header)}
	err := c.Proxy(context.Background(), router.Decision{Model: "claude-opus-4-7"}, prep, httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")))
	require.NoError(t, err)
	assert.Equal(t, "vendor-opus", gotBody["model"], "routed dispatch keeps the catalog upstream-ID rewrite")
}

func TestProxy_PreserveNativeStillHonorsBYOKAlias(t *testing.T) {
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_alias"}`))
	}))
	defer upstream.Close()

	c := anthropic.NewClient("deployment-key", upstream.URL)
	ctx := context.WithValue(context.Background(), requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey:       []byte("byok-key"),
		Source:       "byok",
		ModelAliases: map[string]string{"claude-opus-4-7": "tenant-opus"},
	})
	prep := providers.PreparedRequest{Body: []byte(preservedMessagesBody), Headers: make(http.Header), PreserveNative: true}
	err := c.Proxy(ctx, router.Decision{Model: "claude-opus-4-7"}, prep, httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")))
	require.NoError(t, err)
	assert.Equal(t, "tenant-opus", gotBody["model"], "the BYOK endpoint alias is a provider-required contract and still applies")
}

func TestProxy_PreserveNativeDoesNotProbeAlternateVersionPath(t *testing.T) {
	var hits atomic.Int32
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		paths = append(paths, r.URL.Path)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"model not found"}}`))
	}))
	defer upstream.Close()

	// A base URL that already carries "/v1" disagrees with the "/v1/messages"
	// suffix, which is exactly when the adapter would normally probe twice.
	gatewayBase := upstream.URL + "/v1"
	prep := providers.PreparedRequest{Body: []byte(`{"model":"claude-opus-4-7","messages":[]}`), Headers: make(http.Header)}
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	routed := anthropic.NewClient("k", gatewayBase)
	err := routed.Proxy(context.Background(), router.Decision{Model: "claude-opus-4-7"}, prep, httptest.NewRecorder(), clientReq)
	require.Error(t, err)
	require.Equal(t, int32(2), hits.Load(), "routed dispatch probes the alternate path on 404")

	hits.Store(0)
	paths = nil
	prep.PreserveNative = true
	preserved := anthropic.NewClient("k", gatewayBase)
	rec := httptest.NewRecorder()
	err = preserved.Proxy(context.Background(), router.Decision{Model: "claude-opus-4-7"}, prep, rec, clientReq)
	require.Error(t, err)
	assert.True(t, providers.IsUpstreamModelNotFound(err), "the single upstream failure is surfaced, not masked")
	assert.Equal(t, int32(1), hits.Load(), "a preserved native request must issue exactly one upstream attempt")
	assert.Equal(t, []string{"/v1/v1/messages"}, paths)
	assert.Zero(t, rec.Body.Len(), "the buffered error must not reach the writer")
}
