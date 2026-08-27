package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/providers/anthropic"
	"workweave/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxy_RedirectIsNotRelayedToClient: a 3xx from the Anthropic upstream
// must become a retryable synthesized 502, never a relayed Location the
// client would follow with its own key and the prompt body.
func TestProxy_RedirectIsNotRelayedToClient(t *testing.T) {
	var targetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()

	c := anthropic.NewClient("deployment-key", upstream.URL)
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: []byte(`{"model":"x"}`), Headers: make(http.Header)}

	err := c.Proxy(context.Background(), router.Decision{Model: "claude-opus-4-8"}, prep, rec, clientReq)

	require.Error(t, err)
	var buffered *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &buffered, "a refused redirect must buffer so the dispatcher can fail over")
	assert.Equal(t, http.StatusBadGateway, buffered.Status)
	assert.Empty(t, buffered.Headers.Get("Location"), "the redirect Location must not survive into the buffered error")
	assert.Zero(t, rec.Body.Len(), "writer must not be touched on a refused redirect")
	assert.Empty(t, rec.Header().Get("Location"), "the redirect Location must never reach the client")
	assert.False(t, targetHit.Load(), "the redirect target must never be contacted")
}

// TestListModels_RedirectRefused: a redirecting /v1/models must fail loud
// instead of feeding the redirect boilerplate to the roster parser.
func TestListModels_RedirectRefused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://unconfigured.example"+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer upstream.Close()

	c := anthropic.NewClient("deployment-key", upstream.URL)
	ids, err := c.ListModels(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirect")
	assert.Empty(t, ids)
}
