package auth

import (
	"context"
	"database/sql"
	"errors"
)

type RoutingCredentialLookup interface {
	GetActiveByHashWithInstallation(context.Context, string) (*APIKey, *Installation, error)
}

type RoutingCredentialVerifier struct {
	Keys     RoutingCredentialLookup
	Subjects CredentialSubjectLookup
}

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
