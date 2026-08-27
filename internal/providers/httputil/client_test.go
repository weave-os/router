package httputil

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClientDoesNotFollowRedirects(t *testing.T) {
	var redirectTargetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	client := NewClient(NewTransport(time.Second, time.Second))
	req, err := http.NewRequest(http.MethodGet, redirector.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode, "the 3xx is returned to the caller rather than followed")
	assert.Equal(t, target.URL, resp.Header.Get("Location"))
	assert.False(t, redirectTargetHit.Load(), "the redirect target must never be contacted")
}

func TestIsRedirect(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		assert.True(t, IsRedirect(status), "status %d", status)
	}
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusBadRequest, http.StatusBadGateway} {
		assert.False(t, IsRedirect(status), "status %d", status)
	}
}
