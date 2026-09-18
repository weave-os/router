package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// VerifyAnalyticsCredential authenticates the read-only gateway surface without
// granting routing admission or reusing a possibly stale positive auth cache.
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
