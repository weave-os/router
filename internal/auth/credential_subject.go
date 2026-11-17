package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrPersonalCredentialRequired = errors.New("personal credentials require authenticated account-owned setup or rotation")

type CredentialSubject struct {
	ID                   string
	ProjectionComplete   bool
	InternalEnrolled     bool
	EnrollmentGeneration int64
	AccessEnabled        bool
	RevokedAt            *time.Time
}

type CredentialSubjectLookup interface {
	GetCredentialSubject(context.Context, string, string) (*CredentialSubject, error)
}

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

func ValidateServingCredential(key APIKey, subject *CredentialSubject) error {
	if key.Scope == ScopeSessionIssuer {
		if key.CredentialSubjectID != "" || subject != nil {
			return ErrPersonalCredentialRequired
		}
		return nil
	}
	return ValidateCredentialSubject(key, subject)
}
