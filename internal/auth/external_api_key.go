package auth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ExternalAPIKey represents a customer-owned provider API key.
type ExternalAPIKey struct {
	ID             string
	InstallationID string
	Provider       string // one of providers.Provider* — see providers.APIKeyEnvVars
	Name           *string
	KeyPrefix      string
	KeySuffix      string
	KeyFingerprint string
	// BaseURL overrides the provider's deployment endpoint for this key; non-empty on BYOK keys only.
	BaseURL string
	// ModelAliases maps a catalog model ID to the upstream name this key's endpoint publishes.
	// Nil means unchanged; routing, pricing, and telemetry always key off the catalog ID.
	ModelAliases map[string]string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
	// Plaintext is populated after decrypt; never logged.
	Plaintext []byte
}

type CreateExternalAPIKeyParams struct {
	InstallationID string
	ExternalID     string
	Provider       string
	KeyCiphertext  []byte
	KeyPrefix      string
	KeySuffix      string
	KeyFingerprint string
	Name           *string
	BaseURL        *string
	ModelAliases   map[string]string
	CreatedBy      *string
}

// maxModelAliases bounds one key's alias map: generous next to any real
// catalog, and keeps a pathological payload out of the auth cache.
const maxModelAliases = 256

// maxModelAliasLength bounds a single alias, matching the model-id column width.
const maxModelAliasLength = 255

// NormalizeModelAliases trims and validates entries against allowed catalog IDs (nil skips).
// Returns nil when nothing survives so "no aliases" has one representation.
func NormalizeModelAliases(raw map[string]string, allowed map[string]struct{}) (map[string]string, error) {
	if len(raw) > maxModelAliases {
		return nil, fmt.Errorf("%w: %d entries exceeds the limit of %d", ErrInvalidModelAlias, len(raw), maxModelAliases)
	}
	out := make(map[string]string, len(raw))
	for model, alias := range raw {
		model = strings.TrimSpace(model)
		alias = strings.TrimSpace(alias)
		if model == "" || alias == "" {
			continue
		}
		if len(alias) > maxModelAliasLength {
			return nil, fmt.Errorf("%w: alias for %q exceeds %d characters", ErrInvalidModelAlias, model, maxModelAliasLength)
		}
		if allowed != nil {
			if _, ok := allowed[model]; !ok {
				return nil, fmt.Errorf("%w: %q", ErrUnknownModel, model)
			}
		}
		out[model] = alias
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// NormalizeBaseURL validates and normalizes a BYOK endpoint override: trims whitespace,
// strips trailing slashes (providers append their own path), and rejects non-absolute http(s) URLs.
func NormalizeBaseURL(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimRight(strings.TrimSpace(*raw), "/")
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidBaseURL, trimmed)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidBaseURL, trimmed)
	}
	return &trimmed, nil
}

// ExternalAPIKeyRepository manages external API keys in storage.
type ExternalAPIKeyRepository interface {
	Create(ctx context.Context, params CreateExternalAPIKeyParams) (*ExternalAPIKey, error)
	// GetForInstallation returns all active keys with Plaintext populated.
	GetForInstallation(ctx context.Context, installationID string) ([]*ExternalAPIKey, error)
	SoftDeleteByProvider(ctx context.Context, installationID, provider string) error
	SoftDelete(ctx context.Context, installationID, id string) error
	MarkUsed(ctx context.Context, id string) error
}
