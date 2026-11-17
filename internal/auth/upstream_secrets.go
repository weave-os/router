package auth

import (
	"context"

	"weave-os/router/internal/observability"
)

func (s *Service) resolveUpstreamSecrets(ctx context.Context, keys []*ExternalAPIKey) []*ExternalAPIKey {
	if !anyDerivedAuth(keys) {
		return keys
	}
	resolved := make([]*ExternalAPIKey, 0, len(keys))
	for _, key := range keys {
		if key.AuthType != AuthTypeKeypairJWT && key.AuthType != AuthTypeWIF && key.AuthType != AuthTypeAnthropicWIF && key.AuthType != AuthTypeAzureEntra {
			resolved = append(resolved, key)
			continue
		}
		credential, err := s.upstreamCredential(ctx, key)
		if err == nil && key.AuthType == AuthTypeAnthropicWIF && len(credential) == 0 {
			err = ErrAnthropicFederationUnavailable
		}
		if err != nil {
			observability.FromContext(ctx).Warn("Failed to resolve upstream credential for external API key",
				"external_api_key_id", key.ID, "provider", key.Provider, "auth_type", key.AuthType, "err", err)
			if key.AuthType == AuthTypeAnthropicWIF {
				unavailable := *key
				unavailable.CredentialError = ErrAnthropicFederationUnavailable
				resolved = append(resolved, &unavailable)
			}
			continue
		}
		withCredential := *key
		withCredential.Plaintext = credential
		if key.AuthType == AuthTypeAnthropicWIF {
			withCredential.BaseURL = AnthropicFederationAudience
		}
		resolved = append(resolved, &withCredential)
	}
	return resolved
}

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
			if len(resolved) > 0 && resolved[0].CredentialError != nil {
				return nil, resolved[0].CredentialError
			}
			return nil, ErrUpstreamCredentialUnavailable
		}
		return resolved[0], nil
	}
	return nil, ErrExternalAPIKeyNotFound
}

func (s *Service) upstreamCredential(ctx context.Context, key *ExternalAPIKey) ([]byte, error) {
	switch key.AuthType {
	case AuthTypeAnthropicWIF:
		if s.anthropicFederationTokens == nil {
			return nil, ErrAnthropicFederationUnavailable
		}
		return s.anthropicFederationTokens.Token(ctx, key)
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

func anyDerivedAuth(keys []*ExternalAPIKey) bool {
	for _, key := range keys {
		if key.AuthType == AuthTypeKeypairJWT || key.AuthType == AuthTypeWIF || key.AuthType == AuthTypeAnthropicWIF || key.AuthType == AuthTypeAzureEntra {
			return true
		}
	}
	return false
}
