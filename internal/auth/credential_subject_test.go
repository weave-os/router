package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

func TestCredentialOwnershipCannotBeAssertedForSharedKey(t *testing.T) {
	key := auth.APIKey{ID: "shared", Scope: auth.ScopeRouting}
	subject := auth.CredentialSubject{ID: "subject", ProjectionComplete: true, AccessEnabled: true, InternalEnrolled: true}
	require.NoError(t, auth.ValidateCredentialSubject(key, nil))
	require.ErrorIs(t, auth.ValidateCredentialSubject(key, &subject), auth.ErrPersonalCredentialRequired)
	key.CreatedBy = &subject.ID
	require.ErrorIs(t, auth.ValidateCredentialSubject(key, &subject), auth.ErrPersonalCredentialRequired)
}

func TestPersonalCredentialRequiresCompletedUnrevokedProjection(t *testing.T) {
	key := auth.APIKey{ID: "personal", CredentialSubjectID: "subject", Scope: auth.ScopeRouting}
	valid := auth.CredentialSubject{ID: "subject", ProjectionComplete: true, AccessEnabled: true, InternalEnrolled: true, EnrollmentGeneration: 1}
	require.NoError(t, auth.ValidateCredentialSubject(key, &valid))
	for _, change := range []func(*auth.CredentialSubject){
		func(s *auth.CredentialSubject) { s.ID = "someone-else" },
		func(s *auth.CredentialSubject) { s.ProjectionComplete = false },
		func(s *auth.CredentialSubject) { s.AccessEnabled = false },
		func(s *auth.CredentialSubject) { now := time.Now(); s.RevokedAt = &now },
	} {
		subject := valid
		change(&subject)
		require.ErrorIs(t, auth.ValidateCredentialSubject(key, &subject), auth.ErrPersonalCredentialRequired)
	}
	key.Scope = auth.ScopeAnalyticsRead
	require.ErrorIs(t, auth.ValidateCredentialSubject(key, &valid), auth.ErrInvalidKeyScope)
}

func TestServingAdmissionValidatesSessionIssuerSubjectBinding(t *testing.T) {
	issuer := auth.APIKey{ID: "issuer", Scope: auth.ScopeSessionIssuer}
	valid := auth.CredentialSubject{ID: "subject", ProjectionComplete: true, AccessEnabled: true, InternalEnrolled: true, EnrollmentGeneration: 1}
	require.NoError(t, auth.ValidateServingCredential(issuer, nil))
	require.ErrorIs(t, auth.ValidateServingCredential(issuer, &valid), auth.ErrPersonalCredentialRequired)
	issuer.CredentialSubjectID = valid.ID
	require.NoError(t, auth.ValidateServingCredential(issuer, &valid))

	for _, invalid := range []auth.CredentialSubject{
		{ID: "someone-else", ProjectionComplete: true, AccessEnabled: true},
		{ID: "subject", AccessEnabled: true},
		{ID: "subject", ProjectionComplete: true},
		{ID: "subject", ProjectionComplete: true, AccessEnabled: true, EnrollmentGeneration: -1},
	} {
		require.ErrorIs(t, auth.ValidateServingCredential(issuer, &invalid), auth.ErrPersonalCredentialRequired)
	}
	revoked := valid
	now := time.Now()
	revoked.RevokedAt = &now
	require.ErrorIs(t, auth.ValidateServingCredential(issuer, &revoked), auth.ErrPersonalCredentialRequired)

	readKey := auth.APIKey{ID: "read", Scope: auth.ScopeAnalyticsRead}
	require.ErrorIs(t, auth.ValidateServingCredential(readKey, nil), auth.ErrInvalidKeyScope)
}
