package auth_test

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_VerifyAPIKey_SessionTokenNeverReachesBearerLookup(t *testing.T) {
	cache := newRecordingAPIKeyCache()
	svc, repo := makeServiceWithCacheAndCounter(t, cache)

	_, _, _, _, err := svc.VerifyAPIKey(context.Background(), auth.SessionTokenPrefix+"_header.payload.signature")

	require.ErrorIs(t, err, auth.ErrInvalidToken)
	assert.Zero(t, repo.getCallCount(), "a session token verifies through VerifySessionToken, never as a hashed bearer")
	assert.Empty(t, cache.sets, "a session token must not leave a negative bearer entry behind")
}

func TestService_VerifyPlatformAPIKey_DoesNotCacheRecordWithoutUpstreamKeys(t *testing.T) {
	const rawToken = "rk_platform_token"
	hash, prefix, suffix := auth.APITokenFingerprint(rawToken)
	installation := &auth.Installation{ID: "inst-platform", ExternalID: "org-platform"}
	row := fakeKeyRow{
		apiKey:       &auth.APIKey{ID: "key-platform", InstallationID: installation.ID, KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix, Scope: auth.ScopeRouting},
		installation: installation,
	}
	cache := newRecordingAPIKeyCache()
	svc, _ := makeServiceWithCacheAndCounter(t, cache, row)

	_, key, _, err := svc.VerifyPlatformAPIKey(context.Background(), rawToken)

	require.NoError(t, err)
	assert.Equal(t, "key-platform", key.ID)
	assert.Empty(t, cache.sets, "a record loaded without BYOK keys would strip them from inference for the positive TTL")
}

func TestService_VerifyAPIKey_RejectsCachedAnalyticsRecord(t *testing.T) {
	const rawToken = "rk_cached_analytics_record"
	keyHash := auth.HashAPIKeySHA256(rawToken)
	installation := &auth.Installation{ID: "inst-cached", ExternalID: "org-cached"}
	cache := newRecordingAPIKeyCache()
	cache.Set(keyHash, auth.CachedKey{
		APIKey:       &auth.APIKey{ID: "key-cached", InstallationID: installation.ID, KeyHash: keyHash, Scope: auth.ScopeAnalyticsRead},
		Installation: installation,
	})
	svc, repo := makeServiceWithCacheAndCounter(t, cache)

	_, _, _, _, err := svc.VerifyAPIKey(context.Background(), rawToken)

	require.ErrorIs(t, err, auth.ErrWrongKeyScope)
	assert.Zero(t, repo.getCallCount())
}
