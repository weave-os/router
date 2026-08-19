package auth

import (
	"context"

	"workweave/router/internal/observability"
)

// resolveUpstreamSecrets replaces each key's Plaintext with its upstream credential.
// Keys that fail to resolve are dropped — never sent as a raw key or empty bearer.
func (s *Service) resolveUpstreamSecrets(ctx context.Context, keys []*ExternalAPIKey) []*ExternalAPIKey {
	if !anyDerivedAuth(keys) {
		return keys
	}
	resolved := make([]*ExternalAPIKey, 0, len(keys))
	for _, key := range keys {
		if key.AuthType != AuthTypeKeypairJWT && key.AuthType != AuthTypeWIF {
			resolved = append(resolved, key)
			continue
		}
		credential, err := s.upstreamCredential(ctx, key)
		if err != nil {
			observability.Get().Warn("Failed to resolve upstream credential for external API key",
				"external_api_key_id", key.ID, "provider", key.Provider, "auth_type", key.AuthType, "err", err)
			continue
		}
		// Copy so the derived credential never overwrites the stored secret held
		// by the shared cache entry these keys came from.
		withCredential := *key
		withCredential.Plaintext = credential
		resolved = append(resolved, &withCredential)
	}
	return resolved
}

// upstreamCredential derives the bearer value for a key whose stored secret is not itself
// the credential.
func (s *Service) upstreamCredential(ctx context.Context, key *ExternalAPIKey) ([]byte, error) {
	if key.AuthType == AuthTypeWIF {
		if s.wifTokens == nil {
			return nil, ErrWIFUnavailable
		}
		return s.wifTokens.Attestation(ctx)
	}
	return s.keypairTokens.Bearer(key)
}

// anyDerivedAuth reports whether any key's credential has to be derived rather than sent as stored.
func anyDerivedAuth(keys []*ExternalAPIKey) bool {
	for _, key := range keys {
		if key.AuthType == AuthTypeKeypairJWT || key.AuthType == AuthTypeWIF {
			return true
		}
	}
	return false
}
