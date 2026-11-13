package auth_test

import (
	"context"
	"strings"
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func analyticsKeyRow(t *testing.T, rawToken string) (fakeKeyRow, *auth.Installation) {
	t.Helper()
	hash, prefix, suffix := auth.APITokenFingerprint(rawToken)
	installation := &auth.Installation{ID: "inst-analytics", ExternalID: "org-analytics"}
	return fakeKeyRow{
		apiKey: &auth.APIKey{
			ID:             "key-analytics",
			InstallationID: installation.ID,
			KeyHash:        hash,
			KeyPrefix:      prefix,
			KeySuffix:      suffix,
			Scope:          auth.ScopeAnalyticsRead,
		},
		installation: installation,
	}, installation
}

func TestService_VerifyAPIKey_RejectsAnalyticsScopedKey(t *testing.T) {
	const rawToken = "ra_export_token"
	row, _ := analyticsKeyRow(t, rawToken)
	svc, apiKeys := makeService(t, row)

	_, _, _, _, err := svc.VerifyAPIKey(context.Background(), rawToken)

	require.ErrorIs(t, err, auth.ErrWrongKeyScope,
		"an analytics key must not authenticate the data plane, or an ETL credential could spend money")
	assert.Empty(t, apiKeys.markUsedSnapshot(),
		"a rejected key must not be recorded as used on the routing path")
}

func TestService_VerifyAnalyticsAPIKey_RejectsRoutingScopedKey(t *testing.T) {
	const rawToken = "rk_routing_token"
	hash, prefix, suffix := auth.APITokenFingerprint(rawToken)
	installation := &auth.Installation{ID: "inst-1", ExternalID: "org-1"}
	svc, _ := makeService(t, fakeKeyRow{
		apiKey: &auth.APIKey{
			ID: "key-routing", InstallationID: installation.ID,
			KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix,
			Scope: auth.ScopeRouting,
		},
		installation: installation,
	})

	_, _, err := svc.VerifyAnalyticsAPIKey(context.Background(), rawToken)

	require.ErrorIs(t, err, auth.ErrInvalidPrefix,
		"an rk_ token must be turned away at the export surface before any lookup")
}

// A routing key that somehow carries an ra_ prefix must still be refused on the
// scope, not just the prefix — the persisted scope is the authority.
func TestService_VerifyAnalyticsAPIKey_RejectsWrongScopeBehindAnalyticsPrefix(t *testing.T) {
	const rawToken = "ra_mislabeled_token"
	hash, prefix, suffix := auth.APITokenFingerprint(rawToken)
	installation := &auth.Installation{ID: "inst-1", ExternalID: "org-1"}
	svc, _ := makeService(t, fakeKeyRow{
		apiKey: &auth.APIKey{
			ID: "key-routing", InstallationID: installation.ID,
			KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix,
			Scope: auth.ScopeRouting,
		},
		installation: installation,
	})

	_, _, err := svc.VerifyAnalyticsAPIKey(context.Background(), rawToken)

	require.ErrorIs(t, err, auth.ErrWrongKeyScope)
}

func TestService_VerifyAnalyticsAPIKey_HappyPath(t *testing.T) {
	const rawToken = "ra_export_token"
	row, wantInstallation := analyticsKeyRow(t, rawToken)
	svc, _ := makeService(t, row)

	gotInstallation, gotKey, err := svc.VerifyAnalyticsAPIKey(context.Background(), rawToken)

	require.NoError(t, err)
	assert.Equal(t, wantInstallation, gotInstallation)
	require.NotNil(t, gotKey)
	assert.Equal(t, auth.ScopeAnalyticsRead, gotKey.Scope)
}

func TestService_VerifyAnalyticsAPIKey_RejectsUnknownToken(t *testing.T) {
	svc, _ := makeService(t)

	_, _, err := svc.VerifyAnalyticsAPIKey(context.Background(), "ra_unknown")

	require.ErrorIs(t, err, auth.ErrInvalidToken)
}

// Pre-scope keys were backfilled to 'routing', so a zero-valued scope must keep
// routing — and must never be read as analytics access.
func TestAPIKeyScope_ZeroValueIsRouting(t *testing.T) {
	var zero auth.APIKeyScope

	assert.Equal(t, auth.ScopeRouting, zero.Normalized())
	assert.True(t, zero.Valid())
	assert.False(t, auth.APIKeyScope("something_else").Valid())
}

func TestService_IssueScopedAPIKey_PrefixFollowsScope(t *testing.T) {
	tests := []struct {
		scope      auth.APIKeyScope
		wantPrefix string
	}{
		{auth.ScopeRouting, auth.APIKeyPrefix + "_"},
		{auth.ScopeAnalyticsRead, auth.AnalyticsAPIKeyPrefix + "_"},
	}
	for _, tt := range tests {
		t.Run(string(tt.scope), func(t *testing.T) {
			svc, apiKeys := makeService(t)
			apiKeys.echoCreate = true

			key, rawToken, err := svc.IssueScopedAPIKey(context.Background(), "inst-1", tt.scope, nil, nil)

			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(rawToken, tt.wantPrefix),
				"token %q must be fronted by %q so the credential advertises its own authority", rawToken, tt.wantPrefix)
			assert.Equal(t, tt.scope, key.Scope)
		})
	}
}

func TestService_IssueScopedAPIKey_RejectsUnknownScope(t *testing.T) {
	svc, _ := makeService(t)

	_, _, err := svc.IssueScopedAPIKey(context.Background(), "inst-1", auth.APIKeyScope("admin"), nil, nil)

	require.ErrorIs(t, err, auth.ErrInvalidKeyScope)
}
