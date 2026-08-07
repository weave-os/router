package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// VerifyAnalyticsAPIKey authenticates a raw bearer token for the read-only
// analytics export. It returns ErrInvalidPrefix for a token that isn't an ra_
// key, ErrInvalidToken when no active key matches, and ErrWrongKeyScope when a
// live key is not analytics-scoped.
//
// Deliberately narrower than VerifyAPIKey: no BYOK secrets and no per-cluster
// allowlists are read, because nothing on the export surface may dispatch a
// request. Analytics and routing tokens hash to different cache entries, so the
// slimmer cached record can never be picked up by the data plane.
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
