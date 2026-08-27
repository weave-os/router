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

func TestNewClientFailsTheCallOnARedirect(t *testing.T) {
	// atomic: written on the httptest handler goroutine, read here.
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

	require.Error(t, err, "a refused redirect fails the call rather than yielding a relayable response")
	// http.Client wraps CheckRedirect's error in *url.Error; the sentinel has to
	// survive that for dispatch classification to recognize it.
	assert.ErrorIs(t, err, ErrRefusedRedirect)
	assert.False(t, redirectTargetHit.Load(), "the redirect target must never be contacted")

	// Do returns the pre-redirect response alongside the error with its body already
	// closed; pin that it is unusable even if an adapter ever skipped the err check.
	require.NotNil(t, resp)
	n, readErr := resp.Body.Read(make([]byte, 1))
	assert.Zero(t, n)
	assert.Error(t, readErr, "the returned response body is closed")
}
