package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Service) VerifyAnalyticsAPIKey(ctx context.Context, rawToken string) (*Installation, *APIKey, error) {
	if !strings.HasPrefix(rawToken, AnalyticsAPIKeyPrefix+"_") {
		return nil, nil, ErrInvalidPrefix
	}
	return s.verifySlimAPIKey(ctx, rawToken, ScopeAnalyticsRead)
}

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

	if scope == ScopeAnalyticsRead {
		s.cache.Set(keyHash, CachedKey{APIKey: apiKey, Installation: installation})
	}
	s.fireMarkUsed(apiKey, installation)
	return installation, apiKey, nil
}
