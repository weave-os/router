package openaicompat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/providers/httputil"
	"workweave/router/internal/providers/openaicompat"
	"workweave/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redirectFixture stands up a redirecting upstream and an isolated target,
// so tests can assert the router never contacts the unconfigured host.
func redirectFixture(t *testing.T) (upstream *httptest.Server, targetHit *atomic.Bool) {
	t.Helper()
	var hit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(target.Close)
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(upstream.Close)
	return upstream, &hit
}

// TestProxy_RefusedRedirectFailsRetryablyWithoutTouchingWriter: refused
// redirect fails retryably; Location never reaches the client.
func TestProxy_RefusedRedirectFailsRetryablyWithoutTouchingWriter(t *testing.T) {
	upstream, targetHit := redirectFixture(t)

	c := openaicompat.NewClient("test-key", upstream.URL+"/api/v1")
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: []byte(`{"model":"m","messages":[]}`), Headers: make(http.Header)}

	err := c.Proxy(context.Background(), router.Decision{Model: "m"}, prep, rec, clientReq)

	require.Error(t, err)
	assert.ErrorIs(t, err, httputil.ErrRefusedRedirect, "the sentinel must survive the adapter's wrapping for classification")
	assert.True(t, providers.IsRetryable(err), "no bytes reached the client, so the next binding may be tried")
	assert.Zero(t, rec.Body.Len(), "writer must not be touched on a refused redirect")
	assert.Empty(t, rec.Header().Get("Location"), "the redirect Location must never reach the client")
	assert.False(t, targetHit.Load(), "the redirect target must never be contacted")
}

// TestPassthrough_RefusedRedirectRelaysNothing: same invariant on the
// passthrough path, which writes directly on success.
func TestPassthrough_RefusedRedirectRelaysNothing(t *testing.T) {
	upstream, targetHit := redirectFixture(t)

	c := openaicompat.NewClient("test-key", upstream.URL+"/api/v1")
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	prep := providers.PreparedRequest{Headers: make(http.Header)}

	err := c.Passthrough(context.Background(), prep, rec, clientReq)

	require.Error(t, err)
	assert.ErrorIs(t, err, httputil.ErrRefusedRedirect)
	assert.Zero(t, rec.Body.Len(), "no redirect body reaches the caller")
	assert.Empty(t, rec.Header().Get("Location"), "the unconfigured host must not reach the caller")
	assert.False(t, targetHit.Load(), "the redirect target must never be contacted")
}

// TestListModels_RedirectRefused: roster fetch must fail loud instead of
// parsing redirect boilerplate into an empty model list.
func TestListModels_RedirectRefused(t *testing.T) {
	upstream, targetHit := redirectFixture(t)

	c := openaicompat.NewClient("test-key", upstream.URL+"/v1")
	ids, err := c.ListModels(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, httputil.ErrRefusedRedirect)
	assert.Empty(t, ids)
	assert.False(t, targetHit.Load(), "the redirect target must never be contacted")
}
