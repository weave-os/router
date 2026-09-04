package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxy_RefusedRedirectFailsRetryablyWithoutTouchingWriter: routed-path
// counterpart of TestPassthrough_RelaysNothingFromARefusedRedirect.
func TestProxy_RefusedRedirectFailsRetryablyWithoutTouchingWriter(t *testing.T) {
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
	assert.ErrorIs(t, err, httputil.ErrRefusedRedirect, "the sentinel must survive the adapter's wrapping for classification")
	assert.True(t, providers.IsRetryable(err), "no bytes reached the client, so the next binding may be tried")
	assert.Zero(t, rec.Body.Len(), "writer must not be touched on a refused redirect")
	assert.Empty(t, rec.Header().Get("Location"), "the redirect Location must never reach the client")
	assert.False(t, targetHit.Load(), "the redirect target must never be contacted")
}

// TestListModels_RedirectRefused: a redirecting /v1/models must fail loud
// instead of feeding redirect boilerplate to the roster parser.
func TestListModels_RedirectRefused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://unconfigured.example"+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer upstream.Close()

	c := anthropic.NewClient("deployment-key", upstream.URL)
	ids, err := c.ListModels(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, httputil.ErrRefusedRedirect)
	assert.Empty(t, ids)
}
