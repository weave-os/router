package oidcjwks_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/oidcjwks"
)

type issuerServer struct {
	server        *httptest.Server
	keys          atomic.Pointer[jose.JSONWebKeySet]
	jwksHits      atomic.Int32
	discoveryHits atomic.Int32
	failing       atomic.Bool
	issuerName    string
}

func newIssuerServer(t *testing.T) *issuerServer {
	t.Helper()
	s := &issuerServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		s.discoveryHits.Add(1)
		if s.failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		issuer := s.issuerName
		if issuer == "" {
			issuer = s.server.URL
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": s.server.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		s.jwksHits.Add(1)
		_ = json.NewEncoder(w).Encode(s.keys.Load())
	})
	s.server = httptest.NewTLSServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *issuerServer) publish(t *testing.T, kid string) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	s.keys.Store(&jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"}}})
	return key
}

func TestKeySetResolvesKeysThroughDiscovery(t *testing.T) {
	issuer := newIssuerServer(t)
	signing := issuer.publish(t, "k1")
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	keys := oidcjwks.New(issuer.server.Client(), func() time.Time { return now })

	got, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.NoError(t, err)
	assert.Equal(t, &signing.PublicKey, got)

	_, err = keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.NoError(t, err)
	assert.Equal(t, int32(1), issuer.jwksHits.Load(), "a cached set serves repeat lookups")
}

func TestKeySetRefetchesOnRotationButRateLimitsUnknownKids(t *testing.T) {
	issuer := newIssuerServer(t)
	issuer.publish(t, "k1")
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	keys := oidcjwks.New(issuer.server.Client(), func() time.Time { return now })
	_, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.NoError(t, err)

	_, err = keys.VerificationKey(context.Background(), issuer.server.URL, "unknown")
	require.ErrorIs(t, err, auth.ErrInvalidToken)
	assert.Equal(t, int32(1), issuer.jwksHits.Load(), "unknown kids inside the refresh interval do not hit the issuer")

	rotated := issuer.publish(t, "k2")
	now = now.Add(2 * time.Minute)
	got, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k2")
	require.NoError(t, err)
	assert.Equal(t, &rotated.PublicKey, got)
	assert.Equal(t, int32(2), issuer.jwksHits.Load())
}

func TestKeySetRejectsAMismatchedDiscoveryIssuer(t *testing.T) {
	issuer := newIssuerServer(t)
	issuer.publish(t, "k1")
	issuer.issuerName = "https://someone-else.example"
	keys := oidcjwks.New(issuer.server.Client(), nil)

	_, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.ErrorIs(t, err, oidcjwks.ErrFetch)
	assert.Zero(t, issuer.jwksHits.Load())
}

func TestKeySetServesLastGoodKeysWhileTheIssuerIsUnreachable(t *testing.T) {
	issuer := newIssuerServer(t)
	issuer.publish(t, "k1")
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	keys := oidcjwks.New(issuer.server.Client(), func() time.Time { return now })
	_, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.NoError(t, err)

	issuer.failing.Store(true)
	now = now.Add(oidcjwks.DefaultTTL + time.Second)
	_, err = keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.NoError(t, err, "a refresh outage must not reject a key that was last served by the issuer")
	_, err = keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.NoError(t, err, "the last-good key keeps working while refreshes are backing off")
	assert.Equal(t, int32(2), issuer.discoveryHits.Load(), "backoff still rate limits refreshes during the outage")

	now = now.Add(2 * time.Minute)
	_, err = keys.VerificationKey(context.Background(), issuer.server.URL, "unknown")
	require.ErrorIs(t, err, oidcjwks.ErrFetch, "a key the issuer never served must not be trusted during an outage")

	issuer.failing.Store(false)
	rotated := issuer.publish(t, "k2")
	now = now.Add(2 * time.Minute)
	got, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k2")
	require.NoError(t, err, "traffic recovers once the issuer is reachable again")
	assert.Equal(t, &rotated.PublicKey, got)
}

func TestKeySetFailsClosedWhenLastGoodKeysOutliveTheGrace(t *testing.T) {
	issuer := newIssuerServer(t)
	issuer.publish(t, "k1")
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	keys := oidcjwks.New(issuer.server.Client(), func() time.Time { return now })
	_, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.NoError(t, err)

	issuer.failing.Store(true)
	now = now.Add(oidcjwks.StaleKeyGrace)
	_, err = keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.ErrorIs(t, err, oidcjwks.ErrFetch, "keys older than the grace must not be trusted while the issuer is unreachable")
}

func TestKeySetDropsLastGoodKeysWhenDiscoveryNamesAnotherIssuer(t *testing.T) {
	issuer := newIssuerServer(t)
	issuer.publish(t, "k1")
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	keys := oidcjwks.New(issuer.server.Client(), func() time.Time { return now })
	_, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.NoError(t, err)

	issuer.issuerName = "https://someone-else.example"
	now = now.Add(oidcjwks.DefaultTTL + time.Second)
	_, err = keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.ErrorIs(t, err, oidcjwks.ErrFetch, "a discovery document naming another issuer is not an outage")
	_, err = keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.ErrorIs(t, err, oidcjwks.ErrFetch, "the refusal holds while refreshes are backing off")
}

func TestKeySetBacksOffAfterAFailedRefresh(t *testing.T) {
	issuer := newIssuerServer(t)
	issuer.publish(t, "k1")
	issuer.failing.Store(true)
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	keys := oidcjwks.New(issuer.server.Client(), func() time.Time { return now })

	for range 3 {
		_, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
		require.ErrorIs(t, err, oidcjwks.ErrFetch)
	}
	assert.Equal(t, int32(1), issuer.discoveryHits.Load(), "failed fetches are rate limited like unknown kids")

	issuer.failing.Store(false)
	now = now.Add(2 * time.Minute)
	_, err := keys.VerificationKey(context.Background(), issuer.server.URL, "k1")
	require.NoError(t, err)
}
