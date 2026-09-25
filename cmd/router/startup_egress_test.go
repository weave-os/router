package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type startupEgressTransportFunc func(*http.Request) (*http.Response, error)

func (f startupEgressTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestStartupEgressRejectsInvalidOrigins(t *testing.T) {
	for _, origin := range []string{"http://example.com", "https://", "https://user:secret@example.com", "https://example.com/v1", "https://example.com?token=secret", "https://example.com?", "https://example.com#fragment", "https://example.com,", "https://example.com:bad"} {
		t.Run(origin, func(t *testing.T) {
			_, err := newStartupEgressProbe(origin)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestStartupEgressUnconfiguredDoesNotBlockBoot(t *testing.T) {
	probe, err := newStartupEgressProbe("  ")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, probe.wait(ctx, slog.Default()))
}

func TestStartupEgressUsesUnauthenticatedHEADWithoutRedirects(t *testing.T) {
	var redirects atomic.Int32
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodHead, r.Method)
		assert.Equal(t, "/", r.URL.Path)
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("X-Api-Key"))
		assert.Empty(t, r.Header.Get("Cookie"))
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()
	probe, err := newStartupEgressProbe(" " + origin.URL + "/ ")
	require.NoError(t, err)
	transport := probe.client.Transport.(*http.Transport)
	assert.Nil(t, transport.Proxy)
	assert.True(t, transport.DisableKeepAlives)
	transport.TLSClientConfig = origin.Client().Transport.(*http.Transport).TLSClientConfig
	require.NoError(t, probe.wait(context.Background(), slog.Default()))
	assert.Zero(t, redirects.Load())
}

func TestStartupEgressAcceptsHTTPErrorStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer origin.Close()
			probe, err := newStartupEgressProbe(origin.URL)
			require.NoError(t, err)
			probe.client.Transport.(*http.Transport).TLSClientConfig = origin.Client().Transport.(*http.Transport).TLSClientConfig
			require.NoError(t, probe.wait(context.Background(), slog.Default()))
		})
	}
}

func TestStartupEgressRetriesNetworkFailureUntilRecovery(t *testing.T) {
	probe, err := newStartupEgressProbe("https://example.com")
	require.NoError(t, err)
	var attempts atomic.Int32
	probe.client.Transport = startupEgressTransportFunc(func(r *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("network not ready")
		}
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, probe.wait(ctx, slog.Default()))
	assert.Equal(t, int32(2), attempts.Load())
}

func TestStartupEgressRequiresEveryOriginAndHonorsDeadline(t *testing.T) {
	probe, err := newStartupEgressProbe("https://reachable.example,https://unreachable.example")
	require.NoError(t, err)
	var reachable atomic.Bool
	probe.client.Transport = startupEgressTransportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "reachable.example" {
			reachable.Store(true)
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = probe.wait(ctx, slog.Default())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "unreachable.example")
	assert.True(t, reachable.Load())
}

func TestStartupEgressCancellationStopsRetry(t *testing.T) {
	probe, err := newStartupEgressProbe("https://example.com")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	probe.client.Transport = startupEgressTransportFunc(func(r *http.Request) (*http.Response, error) {
		attempts.Add(1)
		cancel()
		return nil, errors.New("network not ready")
	})
	assert.ErrorIs(t, probe.wait(ctx, slog.Default()), context.Canceled)
	assert.Equal(t, int32(1), attempts.Load())
}

func TestStartupEgressDoesNotSkipTLSVerification(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("untrusted TLS connection reached HTTP handler")
	}))
	defer origin.Close()
	probe, err := newStartupEgressProbe(origin.URL)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, probe.wait(ctx, slog.Default()), context.DeadlineExceeded)
}
