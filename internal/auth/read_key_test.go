package auth_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scopedKeyRow(rawToken string, scope auth.APIKeyScope, installation *auth.Installation) fakeKeyRow {
	hash, prefix, suffix := auth.APITokenFingerprint(rawToken)
	return fakeKeyRow{
		apiKey: &auth.APIKey{
			ID: "key-" + rawToken, InstallationID: installation.ID,
			KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix, Scope: scope,
		},
		installation: installation,
	}
}

func TestService_VerifyReadAPIKey(t *testing.T) {
	installation := &auth.Installation{ID: "inst-read", ExternalID: "org-read"}
	svc, _ := makeService(t,
		scopedKeyRow("rk_routing", auth.ScopeRouting, installation),
		scopedKeyRow("rk_legacy", "", installation),
		scopedKeyRow("ra_analytics", auth.ScopeAnalyticsRead, installation),
		scopedKeyRow("rk_mislabeled", auth.ScopeAnalyticsRead, installation),
		scopedKeyRow("ra_mislabeled", auth.ScopeRouting, installation),
	)

	for _, test := range []struct {
		name, token string
		want        error
	}{
		{"routing key", "rk_routing", nil},
		{"pre-scope routing key", "rk_legacy", nil},
		{"analytics key", "ra_analytics", nil},
		{"analytics scope behind routing prefix", "rk_mislabeled", auth.ErrWrongKeyScope},
		{"routing scope behind analytics prefix", "ra_mislabeled", auth.ErrWrongKeyScope},
		{"unknown or revoked", "rk_unknown", auth.ErrInvalidToken},
		{"foreign prefix", "sk-ant-upstream", auth.ErrInvalidPrefix},
		{"missing", "", auth.ErrInvalidPrefix},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotInstallation, gotKey, err := svc.VerifyReadAPIKey(context.Background(), test.token)
			if test.want != nil {
				require.ErrorIs(t, err, test.want)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, installation, gotInstallation)
			assert.Equal(t, "key-"+test.token, gotKey.ID)
		})
	}
}

// A routing record cached without its BYOK keys would strip them from the next
// inference request for the positive TTL.
func TestService_VerifyReadAPIKey_DoesNotCacheSlimRoutingRecord(t *testing.T) {
	installation := &auth.Installation{ID: "inst-read", ExternalID: "org-read"}
	routing := scopedKeyRow("rk_routing", auth.ScopeRouting, installation)
	analytics := scopedKeyRow("ra_analytics", auth.ScopeAnalyticsRead, installation)
	cache := auth.NewLRUAPIKeyCache(16, 16, time.Minute, time.Minute)
	svc, _ := makeServiceWithCacheAndCounter(t, cache, routing, analytics)

	_, _, err := svc.VerifyReadAPIKey(context.Background(), "rk_routing")
	require.NoError(t, err)
	_, _, err = svc.VerifyReadAPIKey(context.Background(), "ra_analytics")
	require.NoError(t, err)

	_, routingCached := cache.Get(routing.apiKey.KeyHash)
	assert.False(t, routingCached)
	_, analyticsCached := cache.Get(analytics.apiKey.KeyHash)
	assert.True(t, analyticsCached)
}

func TestService_VerifyReadAPIKeyRequiresCurrentPersonalSubject(t *testing.T) {
	for _, test := range []struct {
		name       string
		subject    *auth.CredentialSubject
		lookupErr  error
		withLookup bool
		wantErr    error
	}{
		{"active", &auth.CredentialSubject{ID: "subject", ProjectionComplete: true, AccessEnabled: true, EnrollmentGeneration: 1}, nil, true, nil},
		{"disabled", &auth.CredentialSubject{ID: "subject", ProjectionComplete: true, EnrollmentGeneration: 1}, nil, true, auth.ErrPersonalCredentialRequired},
		{"revoked", &auth.CredentialSubject{ID: "subject", ProjectionComplete: true, AccessEnabled: true, RevokedAt: new(time.Time)}, nil, true, auth.ErrPersonalCredentialRequired},
		{"incomplete projection", &auth.CredentialSubject{ID: "subject", AccessEnabled: true, EnrollmentGeneration: 1}, nil, true, auth.ErrPersonalCredentialRequired},
		{"missing subject", nil, sql.ErrNoRows, true, auth.ErrPersonalCredentialRequired},
		{"lookup unavailable", nil, context.DeadlineExceeded, true, context.DeadlineExceeded},
		{"lookup not configured", nil, nil, false, auth.ErrPersonalCredentialRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := scopedKeyRow("rk_personal", auth.ScopeRouting, &auth.Installation{ID: "installation"})
			row.apiKey.CredentialSubjectID = "subject"
			svc, _ := makeService(t, row)
			if test.withLookup {
				svc.WithCredentialSubjectLookup(credentialSubjectLookupStub{subject: test.subject, err: test.lookupErr})
			}
			_, _, err := svc.VerifyReadAPIKey(context.Background(), "rk_personal")
			if test.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.wantErr)
			}
		})
	}
}

func TestGatewayReadCredentialAcceptsRoutingOrAnalyticsKeys(t *testing.T) {
	for _, test := range []struct {
		name, token     string
		scope           auth.APIKeyScope
		lookupErr, want error
	}{
		{"routing", "rk_test", auth.ScopeRouting, nil, nil},
		{"pre-scope routing", "rk_test", "", nil, nil},
		{"analytics", "ra_test", auth.ScopeAnalyticsRead, nil, nil},
		{"analytics scope behind routing prefix", "rk_test", auth.ScopeAnalyticsRead, nil, auth.ErrWrongKeyScope},
		{"routing scope behind analytics prefix", "ra_test", auth.ScopeRouting, nil, auth.ErrWrongKeyScope},
		{"foreign prefix", "sk-test", auth.ScopeRouting, nil, auth.ErrInvalidPrefix},
		{"revoked", "rk_test", auth.ScopeRouting, sql.ErrNoRows, auth.ErrInvalidToken},
		{"storage unavailable", "ra_test", auth.ScopeAnalyticsRead, context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := auth.RoutingCredentialVerifier{Keys: gatewayCredentialRepo{key: &auth.APIKey{Scope: test.scope}, failure: test.lookupErr}}
			err := verifier.VerifyReadCredential(context.Background(), test.token)
			if test.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.want)
			}
		})
	}
}

type credentialSubjectLookupStub struct {
	subject *auth.CredentialSubject
	err     error
}

func (s credentialSubjectLookupStub) GetCredentialSubject(context.Context, string, string) (*auth.CredentialSubject, error) {
	return s.subject, s.err
}

func TestGatewayReadCredentialRequiresCurrentPersonalSubject(t *testing.T) {
	for _, test := range []struct {
		name    string
		lookup  auth.CredentialSubjectLookup
		wantErr error
	}{
		{"active", credentialSubjectLookupStub{subject: &auth.CredentialSubject{ID: "subject", ProjectionComplete: true, AccessEnabled: true, EnrollmentGeneration: 1}}, nil},
		{"disabled", credentialSubjectLookupStub{subject: &auth.CredentialSubject{ID: "subject", ProjectionComplete: true, EnrollmentGeneration: 1}}, auth.ErrPersonalCredentialRequired},
		{"revoked", credentialSubjectLookupStub{subject: &auth.CredentialSubject{ID: "subject", ProjectionComplete: true, AccessEnabled: true, RevokedAt: new(time.Time)}}, auth.ErrPersonalCredentialRequired},
		{"missing lookup", nil, auth.ErrPersonalCredentialRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := &auth.APIKey{ID: "key", InstallationID: "installation", Scope: auth.ScopeRouting, CredentialSubjectID: "subject"}
			verifier := auth.RoutingCredentialVerifier{Keys: gatewayCredentialRepo{key: key}, Subjects: test.lookup}
			err := verifier.VerifyReadCredential(context.Background(), "rk_test")
			if test.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.wantErr)
			}
		})
	}
}
