package auth

import (
	"context"
	"database/sql"
	"errors"
)

// RoutingCredentialLookup is the gateway's primary read; it exposes no provider secrets or writes.
type RoutingCredentialLookup interface {
	GetActiveByHashWithInstallation(context.Context, string) (*APIKey, *Installation, error)
}

// RoutingCredentialVerifier deliberately has no secret, user-attribution or authorization cache dependencies.
type RoutingCredentialVerifier struct{ Keys RoutingCredentialLookup }

// VerifyRoutingCredential authenticates gateway admission without loading provider secrets or
// caches. Admission rechecks eligibility under the identity/session transaction locks.
func (v RoutingCredentialVerifier) VerifyRoutingCredential(ctx context.Context, rawToken string) (*Installation, *APIKey, error) {
	if !HasAPIKeyPrefix(rawToken) {
		return nil, nil, ErrInvalidPrefix
	}
	key, installation, err := v.Keys.GetActiveByHashWithInstallation(ctx, HashAPIKeySHA256(rawToken))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrInvalidToken
	}
	if err != nil {
		return nil, nil, err
	}
	if key.Scope.Normalized() != ScopeRouting {
		return nil, nil, ErrWrongKeyScope
	}
	return installation, key, nil
}
