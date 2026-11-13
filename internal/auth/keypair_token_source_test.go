package auth_test

import (
	"testing"
	"time"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keypairKeyFor builds a key-pair BYOK row whose secret is the shared test key.
func keypairKeyFor(t *testing.T, id, fingerprint string) *auth.ExternalAPIKey {
	t.Helper()
	return &auth.ExternalAPIKey{
		ID:             id,
		Provider:       "anthropic_gateway",
		KeyFingerprint: fingerprint,
		AuthType:       auth.AuthTypeKeypairJWT,
		AuthAccount:    "MYORG-MYACCOUNT",
		AuthUser:       "SERVICE_USER",
		Plaintext:      pkcs8PEM(t, testKey),
	}
}

func TestKeypairTokenCache_PassesBearerKeysThrough(t *testing.T) {
	cache := auth.NewKeypairTokenCache(time.Now)
	key := &auth.ExternalAPIKey{ID: "ek_1", AuthType: auth.AuthTypeBearer, Plaintext: []byte("pat-secret")}

	bearer, err := cache.Bearer(key)
	require.NoError(t, err)
	assert.Equal(t, []byte("pat-secret"), bearer,
		"a bearer key must reach the upstream untouched — minting only applies to key-pair credentials")
}

func TestKeypairTokenCache_ReusesTokenUntilItAges(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	cache := auth.NewKeypairTokenCache(func() time.Time { return now })
	key := keypairKeyFor(t, "ek_1", "fp-1")

	first, err := cache.Bearer(key)
	require.NoError(t, err)
	second, err := cache.Bearer(key)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second),
		"a cached token must be reused so a busy installation doesn't sign an RSA JWT per request")

	now = now.Add(50 * time.Minute)
	third, err := cache.Bearer(key)
	require.NoError(t, err)
	assert.NotEqual(t, string(first), string(third),
		"the cached token must be replaced before it reaches Snowflake's one-hour ceiling")
}

func TestKeypairTokenCache_RemintsAfterKeyRotation(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	cache := auth.NewKeypairTokenCache(func() time.Time { return now })

	first, err := cache.Bearer(keypairKeyFor(t, "ek_1", "fp-1"))
	require.NoError(t, err)
	rotatedKey := keypairKeyFor(t, "ek_1", "fp-2")
	rotatedKey.Plaintext = pkcs8PEM(t, mustGenerateKey(2048))
	rotated, err := cache.Bearer(rotatedKey)
	require.NoError(t, err)

	assert.NotEqual(t, string(first), string(rotated),
		"a rotated secret must invalidate the cached token immediately rather than at its own expiry")
}

func TestKeypairTokenCache_MintsSignedJWT(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	cache := auth.NewKeypairTokenCache(func() time.Time { return now })

	bearer, err := cache.Bearer(keypairKeyFor(t, "ek_1", "fp-1"))
	require.NoError(t, err)

	claims := decodeClaims(t, string(bearer))
	assert.Equal(t, "MYORG-MYACCOUNT.SERVICE_USER", claims["sub"],
		"the minted token must authenticate as the key's configured principal")
}

func TestKeypairTokenCache_FailsOnUnusableSecret(t *testing.T) {
	cache := auth.NewKeypairTokenCache(time.Now)
	key := keypairKeyFor(t, "ek_1", "fp-1")
	key.Plaintext = []byte("pat-secret-not-a-private-key")

	_, err := cache.Bearer(key)
	require.ErrorIs(t, err, auth.ErrInvalidKeypairAuth,
		"a key-pair row holding a non-key secret must fail rather than send the raw secret upstream")
}
