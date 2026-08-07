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

	keyHash := HashAPIKeySHA256(rawToken)

	if cached, ok := s.cache.Get(keyHash); ok {
		if cached.Negative {
			return nil, nil, ErrInvalidToken
		}
		if cached.APIKey != nil {
			if cached.APIKey.Scope != ScopeAnalyticsRead {
				return nil, nil, ErrWrongKeyScope
			}
			s.fireMarkUsed(cached.APIKey.ID)
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

	// Not cached on mismatch: caching a routing key under a slim record would
	// strip its BYOK keys for the rest of the positive TTL.
	if apiKey.Scope != ScopeAnalyticsRead {
		return nil, nil, ErrWrongKeyScope
	}

	s.cache.Set(keyHash, CachedKey{APIKey: apiKey, Installation: installation})
	s.fireMarkUsed(apiKey.ID)
	return installation, apiKey, nil
}
