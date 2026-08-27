package openaicompat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/providers/openaicompat"
	"workweave/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redirectFixture stands up a redirecting upstream plus the host it points
// at, so tests can assert neither the router nor (via a relayed Location)
// the client is steered to the unconfigured target.
func redirectFixture(t *testing.T) (upstream *httptest.Server, targetHit *atomic.Bool, targetURL func() string) {
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
	return upstream, &hit, func() string { return target.URL }
}

// TestProxy_RedirectIsNotRelayedToClient: a 3xx from the upstream must become
// a retryable synthesized 502 — not a relayed redirect. Relaying the Location
// would send the client, carrying its own key and the prompt, to the very
// host the router refused to contact.
func TestProxy_RedirectIsNotRelayedToClient(t *testing.T) {
	upstream, targetHit, targetURL := redirectFixture(t)

	c := openaicompat.NewClient("test-key", upstream.URL+"/api/v1")
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: []byte(`{"model":"m","messages":[]}`), Headers: make(http.Header)}

	err := c.Proxy(context.Background(), router.Decision{Model: "m"}, prep, rec, clientReq)

	require.Error(t, err)
	var buffered *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &buffered, "a refused redirect must buffer so the dispatcher can fail over")
	assert.Equal(t, http.StatusBadGateway, buffered.Status)
	assert.Empty(t, buffered.Headers.Get("Location"), "the redirect Location must not survive into the buffered error")
	assert.True(t, providers.IsRetryable(err), "a redirecting binding must be retryable on the next binding")

	assert.Zero(t, rec.Body.Len(), "writer must not be touched on a refused redirect")
	assert.Empty(t, rec.Header().Get("Location"), "the redirect Location must never reach the client")
	assert.False(t, targetHit.Load(), "the redirect target %s must never be contacted", targetURL())
}

// TestPassthrough_RedirectIsNotRelayedToClient: same invariant on the
// passthrough path, which writes directly — the client sees a synthesized
// 502 envelope, never the 3xx + Location.
func TestPassthrough_RedirectIsNotRelayedToClient(t *testing.T) {
	upstream, targetHit, _ := redirectFixture(t)

	c := openaicompat.NewClient("test-key", upstream.URL+"/api/v1")
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	prep := providers.PreparedRequest{Headers: make(http.Header)}

	err := c.Passthrough(context.Background(), prep, rec, clientReq)

	require.Error(t, err)
	var flushed *providers.UpstreamStatusError
	require.ErrorAs(t, err, &flushed, "passthrough renders the refusal itself and reports bytes written")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"), "the redirect Location must never reach the client")
	assert.Contains(t, rec.Body.String(), "redirect")
	assert.False(t, targetHit.Load(), "the redirect target must never be contacted")
}

// TestListModels_RedirectRefused: the catalog GET is the likeliest place a
// gateway redirects; the roster fetch must fail loud, not parse an empty or
// HTML body into an empty roster.
func TestListModels_RedirectRefused(t *testing.T) {
	upstream, targetHit, _ := redirectFixture(t)

	c := openaicompat.NewClient("test-key", upstream.URL+"/v1")
	ids, err := c.ListModels(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirect")
	assert.Empty(t, ids)
	assert.False(t, targetHit.Load(), "the redirect target must never be contacted")
}
