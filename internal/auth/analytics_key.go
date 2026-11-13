package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// VerifyAnalyticsAPIKey authenticates a raw ra_ bearer token for the read-only
// analytics export surface, returning ErrInvalidPrefix, ErrInvalidToken, or
// ErrWrongKeyScope on failure.
//
// Narrower than VerifyAPIKey: no BYOK fetch, no cluster allowlists — analytics
// and routing tokens hash to separate cache entries so neither surface can pick
// up the other's cached record.
func (s *Service) VerifyAnalyticsAPIKey(ctx context.Context, rawToken string) (*Installation, *APIKey, error) {
	if !strings.HasPrefix(rawToken, AnalyticsAPIKeyPrefix+"_") {
		return nil, nil, ErrInvalidPrefix
	}
	return s.verifySlimAPIKey(ctx, rawToken, ScopeAnalyticsRead)
}

// VerifyReadAPIKey authenticates an rk_ routing key or an ra_ analytics key for
// installation-scoped read surfaces. Both resolve to the owning installation;
// neither loads BYOK secrets, cluster allowlists or subscription state.
func (s *Service) VerifyReadAPIKey(ctx context.Context, rawToken string) (*Installation, *APIKey, error) {
	var installation *Installation
	var apiKey *APIKey
	var err error
	if strings.HasPrefix(rawToken, AnalyticsAPIKeyPrefix+"_") {
		installation, apiKey, err = s.verifySlimAPIKey(ctx, rawToken, ScopeAnalyticsRead)
	} else {
		if !strings.HasPrefix(rawToken, APIKeyPrefix+"_") {
			return nil, nil, ErrInvalidPrefix
		}
		installation, apiKey, err = s.verifySlimAPIKey(ctx, rawToken, ScopeRouting)
	}
	if err != nil {
		return nil, nil, err
	}
	if apiKey.CredentialSubjectID == "" {
		return installation, apiKey, nil
	}
	if s.credentialSubjects == nil {
		return nil, nil, ErrPersonalCredentialRequired
	}
	subject, err := s.credentialSubjects.GetCredentialSubject(ctx, apiKey.CredentialSubjectID, installation.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrPersonalCredentialRequired
	}
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateCredentialSubject(*apiKey, subject); err != nil {
		return nil, nil, err
	}
	return installation, apiKey, nil
}

func (s *Service) verifySlimAPIKey(ctx context.Context, rawToken string, scope APIKeyScope) (*Installation, *APIKey, error) {
	keyHash := HashAPIKeySHA256(rawToken)

	if cached, ok := s.cache.Get(keyHash); ok {
		if cached.Negative {
			return nil, nil, ErrInvalidToken
		}
		if cached.APIKey != nil {
			if cached.APIKey.Scope.Normalized() != scope {
				return nil, nil, ErrWrongKeyScope
			}
			s.fireMarkUsed(cached.APIKey, cached.Installation)
			return cached.Installation, cached.APIKey, nil
		}
	}

	apiKey, installation, err := s.apiKeys.GetActiveByHashWithInstallation(ctx, keyHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.cache.Set(keyHash, CachedKey{Negative: true})
			return nil, nil, ErrInvalidToken
		}
		return nil, nil, err
	}

	if apiKey.Scope.Normalized() != scope {
		return nil, nil, ErrWrongKeyScope
	}

	// Only an analytics record is complete without BYOK keys; caching a slim
	// routing record would strip them from inference for the positive TTL.
	if scope == ScopeAnalyticsRead {
		s.cache.Set(keyHash, CachedKey{APIKey: apiKey, Installation: installation})
	}
	s.fireMarkUsed(apiKey, installation)
	return installation, apiKey, nil
}
