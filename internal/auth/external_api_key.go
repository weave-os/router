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
	BaseURL    string
	CreatedAt  time.Time
	LastUsedAt *time.Time
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
	CreatedBy      *string
}

// NormalizeBaseURL trims and validates a BYOK endpoint override, returning nil
// for nil/blank input. Providers append their own path (e.g. /v1/messages), so
// a trailing slash is stripped to keep the joined URL well-formed. A relative
// or scheme-less value would silently produce an unroutable upstream URL, so it
// returns ErrInvalidBaseURL instead.
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
