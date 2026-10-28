package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrPersonalCredentialRequired prevents legacy installation administration from stripping ownership.
var ErrPersonalCredentialRequired = errors.New("personal credentials require authenticated account-owned setup or rotation")

// CredentialSubject is a minimal router-owned projection; private account IDs never enter this domain.
type CredentialSubject struct {
	ID                   string
	ProjectionComplete   bool
	InternalEnrolled     bool
	EnrollmentGeneration int64
	AccessEnabled        bool
	RevokedAt            *time.Time
}

// CredentialSubjectLookup reads the current installation-scoped eligibility projection.
type CredentialSubjectLookup interface {
	GetCredentialSubject(context.Context, string, string) (*CredentialSubject, error)
}

// ValidateCredentialSubject fails closed on pending/revoked ownership and rejects a shared-key subject.
func ValidateCredentialSubject(key APIKey, subject *CredentialSubject) error {
	if key.Scope.Normalized() != ScopeRouting {
		return ErrInvalidKeyScope
	}
	return validateCredentialSubjectOwnership(key, subject)
}

func validateCredentialSubjectOwnership(key APIKey, subject *CredentialSubject) error {
	if key.CredentialSubjectID == "" {
		if subject != nil {
			return ErrPersonalCredentialRequired
		}
		return nil
	}
	if subject == nil || subject.ID != key.CredentialSubjectID || !subject.ProjectionComplete || !subject.AccessEnabled || subject.RevokedAt != nil || subject.EnrollmentGeneration < 0 {
		return fmt.Errorf("personal credential ownership projection is unavailable or revoked: %w", ErrPersonalCredentialRequired)
	}
	return nil
}

// ValidateServingCredential is ValidateCredentialSubject for serving admission, which
// also admits a session issuer. An issuer row is stored under jwk:<kid>, which no
// bearer hashes to, so it reaches admission only through a verified session token.
func ValidateServingCredential(key APIKey, subject *CredentialSubject) error {
	if key.Scope == ScopeSessionIssuer {
		if key.CredentialSubjectID == "" && subject == nil {
			return nil
		}
		return validateCredentialSubjectOwnership(key, subject)
	}
	return ValidateCredentialSubject(key, subject)
}
