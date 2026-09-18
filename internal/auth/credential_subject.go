package auth

import (
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

// ValidateCredentialSubject fails closed on pending/revoked ownership and rejects a shared-key subject.
func ValidateCredentialSubject(key APIKey, subject *CredentialSubject) error {
	if key.Scope.Normalized() != ScopeRouting {
		return ErrInvalidKeyScope
	}
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
