package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (v RoutingCredentialVerifier) VerifyAnalyticsCredential(ctx context.Context, rawToken string) error {
	if !strings.HasPrefix(rawToken, AnalyticsAPIKeyPrefix+"_") {
		return ErrInvalidPrefix
	}
	key, _, err := v.Keys.GetActiveByHashWithInstallation(ctx, HashAPIKeySHA256(rawToken))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidToken
	}
	if err != nil {
		return err
	}
	if key.Scope != ScopeAnalyticsRead {
		return ErrWrongKeyScope
	}
	return nil
}

func (v RoutingCredentialVerifier) VerifyReadCredential(ctx context.Context, rawToken string) error {
	if strings.HasPrefix(rawToken, AnalyticsAPIKeyPrefix+"_") {
		return v.VerifyAnalyticsCredential(ctx, rawToken)
	}
	if !strings.HasPrefix(rawToken, APIKeyPrefix+"_") {
		return ErrInvalidPrefix
	}
	installation, key, err := v.VerifyRoutingCredential(ctx, rawToken)
	if err != nil {
		return err
	}
	if key.CredentialSubjectID == "" {
		return nil
	}
	if v.Subjects == nil {
		return ErrPersonalCredentialRequired
	}
	subject, err := v.Subjects.GetCredentialSubject(ctx, key.CredentialSubjectID, installation.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPersonalCredentialRequired
	}
	if err != nil {
		return err
	}
	return ValidateCredentialSubject(*key, subject)
}
