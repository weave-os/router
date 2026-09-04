package entra_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/entra"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func entraKey() *auth.ExternalAPIKey {
	return &auth.ExternalAPIKey{
		ID:             "ekid_test",
		KeyFingerprint: "fingerprint-a",
		AuthAccount:    "tenant-id",
		AuthUser:       "client-id",
		Plaintext:      []byte("client-secret"),
	}
}

func TestClientCredentialsSourceToken_MintsAndCachesUntilRefresh(t *testing.T) {
	var calls int32
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, req.Method)
		assert.Equal(t, "/tenant-id/oauth2/v2.0/token", req.URL.Path)
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		values, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		assert.Equal(t, "client-id", values.Get("client_id"))
		assert.Equal(t, "client-secret", values.Get("client_secret"))
		assert.Equal(t, auth.EntraScope, values.Get("scope"))
		assert.Equal(t, "client_credentials", values.Get("grant_type"))
		atomic.AddInt32(&calls, 1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"entra-token","expires_in":100}`)),
			Header:     make(http.Header),
		}, nil
	})}
	current := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	source := entra.NewClientCredentialsSource(client, func() time.Time { return current })
	key := entraKey()

	got, err := source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, []byte("entra-token"), got)
	got, err = source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, []byte("entra-token"), got)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))

	current = current.Add(79 * time.Second)
	_, err = source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "the token is cached until 80%% of expires_in")

	current = current.Add(time.Second)
	_, err = source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "the token refreshes at the configured refresh boundary")
}

func TestClientCredentialsSourceToken_InvalidatesOnFingerprintChange(t *testing.T) {
	var calls int32
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"entra-token","expires_in":3600}`)),
			Header:     make(http.Header),
		}, nil
	})}
	source := entra.NewClientCredentialsSource(client, time.Now)
	key := entraKey()

	_, err := source.Token(context.Background(), key)
	require.NoError(t, err)
	key.KeyFingerprint = "fingerprint-b"
	_, err = source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestClientCredentialsSourceToken_RejectsInvalidResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_client"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	source := entra.NewClientCredentialsSource(client, time.Now)

	_, err := source.Token(context.Background(), entraKey())
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrEntraUnavailable)
	assert.NotContains(t, err.Error(), "client-secret")
}

func TestClientCredentialsSourceToken_ScopesTokenToTheKeyEndpoint(t *testing.T) {
	var scope string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		values, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		scope = values.Get("scope")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"entra-token","expires_in":3600}`)),
			Header:     make(http.Header),
		}, nil
	})}
	source := entra.NewClientCredentialsSource(client, time.Now)

	key := entraKey()
	key.BaseURL = "https://resource.openai.azure.com/openai/v1"
	_, err := source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, auth.EntraScopeCognitiveServices, scope)

	key.KeyFingerprint = "fingerprint-foundry"
	key.BaseURL = "https://resource.services.ai.azure.com/anthropic"
	_, err = source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, auth.EntraScope, scope)
}

func TestClientCredentialsSourceToken_ServesWaiterAfterLeaderCancels(t *testing.T) {
	var once sync.Once
	joined := make(chan struct{})
	release := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		once.Do(func() { close(joined) })
		<-release
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"entra-token","expires_in":3600}`)),
			Header:     make(http.Header),
		}, nil
	})}
	source := entra.NewClientCredentialsSource(client, time.Now)
	key := entraKey()

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	go func() {
		_, _ = source.Token(leaderCtx, key)
	}()
	<-joined

	waiter := make(chan error, 1)
	tokens := make(chan []byte, 1)
	go func() {
		token, err := source.Token(context.Background(), key)
		tokens <- token
		waiter <- err
	}()
	// Give the waiter time to join the in-flight mint before the leader leaves.
	time.Sleep(50 * time.Millisecond)
	cancelLeader()
	close(release)

	require.NoError(t, <-waiter)
	assert.Equal(t, []byte("entra-token"), <-tokens)
}

func TestClientCredentialsSourceToken_RequiresCredentialFields(t *testing.T) {
	source := entra.NewClientCredentialsSource(nil, time.Now)
	cases := []struct {
		name string
		key  *auth.ExternalAPIKey
	}{
		{name: "nil key"},
		{name: "missing tenant", key: &auth.ExternalAPIKey{AuthUser: "client", Plaintext: []byte("secret")}},
		{name: "missing client", key: &auth.ExternalAPIKey{AuthAccount: "tenant", Plaintext: []byte("secret")}},
		{name: "missing secret", key: &auth.ExternalAPIKey{AuthAccount: "tenant", AuthUser: "client"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := source.Token(context.Background(), tc.key)
			require.Error(t, err)
			assert.ErrorIs(t, err, auth.ErrEntraUnavailable)
		})
	}
}
