package auth

import (
	"context"

	"weave-os/router/internal/observability"
)

// resolveUpstreamSecrets replaces each key's Plaintext with its upstream credential.
// Keys that fail to resolve are dropped — never sent as a raw key or empty bearer.
func (s *Service) resolveUpstreamSecrets(ctx context.Context, keys []*ExternalAPIKey) []*ExternalAPIKey {
	if !anyDerivedAuth(keys) {
		return keys
	}
	resolved := make([]*ExternalAPIKey, 0, len(keys))
	for _, key := range keys {
		if key.AuthType != AuthTypeKeypairJWT && key.AuthType != AuthTypeWIF && key.AuthType != AuthTypeAzureEntra {
			resolved = append(resolved, key)
			continue
		}
		credential, err := s.upstreamCredential(ctx, key)
		if err != nil {
			observability.FromContext(ctx).Warn("Failed to resolve upstream credential for external API key",
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

// ExternalAPIKeyWithCredential returns a BYOK key with Plaintext resolved (derived auth included),
// so callers authenticate exactly as an inference call would.
func (s *Service) ExternalAPIKeyWithCredential(ctx context.Context, installationID, id string) (*ExternalAPIKey, error) {
	keys, err := s.externalKeys.GetForInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		if key.ID != id {
			continue
		}
		resolved := s.resolveUpstreamSecrets(ctx, []*ExternalAPIKey{key})
		if len(resolved) == 0 || len(resolved[0].Plaintext) == 0 {
			return nil, ErrUpstreamCredentialUnavailable
		}
		return resolved[0], nil
	}
	return nil, ErrExternalAPIKeyNotFound
}

// upstreamCredential derives the bearer value for a key whose stored secret is not itself
// the credential.
func (s *Service) upstreamCredential(ctx context.Context, key *ExternalAPIKey) ([]byte, error) {
	switch key.AuthType {
	case AuthTypeWIF:
		if s.wifTokens == nil {
			return nil, ErrWIFUnavailable
		}
		return s.wifTokens.Attestation(ctx)
	case AuthTypeAzureEntra:
		if s.entraTokens == nil {
			return nil, ErrEntraUnavailable
		}
		return s.entraTokens.Token(ctx, key)
	default:
		return s.keypairTokens.Bearer(key)
	}
}

// anyDerivedAuth reports whether any key's credential has to be derived rather than sent as stored.
func anyDerivedAuth(keys []*ExternalAPIKey) bool {
	for _, key := range keys {
		if key.AuthType == AuthTypeKeypairJWT || key.AuthType == AuthTypeWIF || key.AuthType == AuthTypeAzureEntra {
			return true
		}
	}
	return false
}
