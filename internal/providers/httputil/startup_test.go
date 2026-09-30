package httputil_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers/httputil"
)

func TestWarmTransportRetainsConnectionWithoutCredentials(t *testing.T) {
	var address string
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			address = r.RemoteAddr
			require.Equal(t, http.MethodHead, r.Method)
			require.Equal(t, "/", r.URL.Path)
			require.Empty(t, r.Header.Get("Authorization"))
			require.Empty(t, r.Header.Get("X-Api-Key"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		require.Equal(t, address, r.RemoteAddr)
		_, _ = io.WriteString(w, "served")
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := httputil.NewClient(transport)
	warmed, err := httputil.WarmTransport(context.Background(), server.URL+"/v1", "", client)
	require.NoError(t, err)
	require.True(t, warmed)
	response, err := client.Get(server.URL + "/v1/models")
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "served", string(body))
	require.Equal(t, int32(2), requests.Load())
}

func TestWarmTransportNeverFollowsRedirect(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()
	warmed, err := httputil.WarmTransport(context.Background(), origin.URL, "", origin.Client())
	require.NoError(t, err)
	require.True(t, warmed)
	require.Zero(t, calls.Load())
}

func TestWarmTransportPreservesTLSAndOriginIsolation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	warmed, err := httputil.WarmTransport(context.Background(), server.URL, "https://another.example", server.Client())
	require.NoError(t, err)
	require.False(t, warmed)
	_, err = httputil.WarmTransport(context.Background(), server.URL, "", http.DefaultClient)
	require.Error(t, err)
	require.Zero(t, calls.Load())
}
