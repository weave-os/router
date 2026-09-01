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
	// IdentityHeader and IdentityHeaderFormat name and shape the header sent to this key's endpoint; empty forwards nothing.
	IdentityHeader       string
	IdentityHeaderFormat string
	// ForwardedClientHeaders are inbound client header names copied verbatim to this key's endpoint.
	ForwardedClientHeaders []string
	// BaggageHeader is this endpoint's JSON baggage header, re-emitted with the
	// caller's email under on-behalf-of; empty forwards nothing.
	BaggageHeader string
	// AuthType is how Plaintext authenticates upstream; see AuthType* constants.
	AuthType string
	// AuthAccount and AuthUser identify the upstream principal; empty unless
	// AuthType is AuthTypeKeypairJWT or AuthTypeAzureEntra.
	AuthAccount string
	AuthUser    string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
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
	// IdentityHeader and IdentityHeaderFormat are set or cleared together.
	IdentityHeader       *string
	IdentityHeaderFormat *string
	// ForwardedClientHeaders and BaggageHeader configure client-header passthrough; nil forwards nothing.
	ForwardedClientHeaders []string
	BaggageHeader          *string
	AuthType               string
	// AuthAccount and AuthUser identify the upstream principal; empty unless
	// AuthType is AuthTypeKeypairJWT or AuthTypeAzureEntra.
	AuthAccount *string
	AuthUser    *string
	CreatedBy   *string
}

// AuthTypeBearer sends the secret verbatim; AuthTypeKeypairJWT signs a short-lived JWT with it;
// AuthTypeWIF stores no secret and attests the router's own workload identity per request;
// AuthTypeAzureEntra exchanges the secret for a short-lived Microsoft Entra bearer token.
const (
	AuthTypeBearer     = "bearer"
	AuthTypeKeypairJWT = "keypair_jwt"
	// AuthTypeAzureEntra exchanges the stored client secret for a short-lived Entra token;
	// AuthAccount is the tenant ID, AuthUser is the client ID.
	AuthTypeAzureEntra = "azure_entra"
	AuthTypeWIF        = "wif"
)

// Identity header formats. IdentityFormatEmail sends the bare address;
// IdentityFormatJSON sends a URL-encoded JSON property bag (display name, session, client app).
const (
	IdentityFormatEmail = "email"
	IdentityFormatJSON  = "json"
)

// maxIdentityHeaderNameLength bounds the configured field name.
const maxIdentityHeaderNameLength = 128

// headersRejectedForIdentity are request-critical headers a tenant must not redirect identity into.
var headersRejectedForIdentity = map[string]struct{}{
	"authorization":  {},
	"x-api-key":      {},
	"host":           {},
	"content-type":   {},
	"content-length": {},
	"accept":         {},
}

// NormalizeIdentityHeader validates the header name and format; both nil clears forwarding.
// Name and format must be set or cleared together.
func NormalizeIdentityHeader(name, format *string) (*string, *string, error) {
	trimmedName := ""
	if name != nil {
		trimmedName = strings.TrimSpace(*name)
	}
	trimmedFormat := ""
	if format != nil {
		trimmedFormat = strings.ToLower(strings.TrimSpace(*format))
	}
	if trimmedName == "" && trimmedFormat == "" {
		return nil, nil, nil
	}
	if trimmedName == "" {
		return nil, nil, fmt.Errorf("%w: a format needs a header name", ErrInvalidIdentityHeader)
	}
	if !validHeaderName(trimmedName) {
		return nil, nil, fmt.Errorf("%w: %q is not a valid header name", ErrInvalidIdentityHeader, trimmedName)
	}
	if _, rejected := headersRejectedForIdentity[strings.ToLower(trimmedName)]; rejected {
		return nil, nil, fmt.Errorf("%w: %q is reserved", ErrInvalidIdentityHeader, trimmedName)
	}
	if trimmedFormat != IdentityFormatEmail && trimmedFormat != IdentityFormatJSON {
		return nil, nil, fmt.Errorf("%w: unknown format %q", ErrInvalidIdentityHeader, trimmedFormat)
	}
	return &trimmedName, &trimmedFormat, nil
}

// maxForwardedClientHeaders bounds one key's passthrough list: enough for a
// vendor's correlation header set, small enough to keep the auth cache tidy.
const maxForwardedClientHeaders = 16

// NormalizeForwardedClientHeaders validates and de-duplicates header names; returns nil when none
// survive so "forwards nothing" has one canonical representation.
func NormalizeForwardedClientHeaders(raw []string) ([]string, error) {
	if len(raw) > maxForwardedClientHeaders {
		return nil, fmt.Errorf("%w: %d headers exceeds the limit of %d", ErrInvalidForwardedHeader, len(raw), maxForwardedClientHeaders)
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, name := range raw {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := validateForwardableHeader(name); err != nil {
			return nil, err
		}
		lower := strings.ToLower(name)
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// NormalizeBaggageHeader validates the JSON baggage header name this endpoint
// reads; nil or blank forwards nothing.
func NormalizeBaggageHeader(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, nil
	}
	if err := validateForwardableHeader(trimmed); err != nil {
		return nil, err
	}
	return &trimmed, nil
}

// validateForwardableHeader rejects malformed names and the request-critical
// ones a caller could otherwise use to redirect its own credentials upstream.
func validateForwardableHeader(name string) error {
	if !validHeaderName(name) {
		return fmt.Errorf("%w: %q is not a valid header name", ErrInvalidForwardedHeader, name)
	}
	if _, rejected := headersRejectedForIdentity[strings.ToLower(name)]; rejected {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidForwardedHeader, name)
	}
	return nil
}

// headerNameChars are the RFC 9110 token characters a field name may contain.
const headerNameChars = "!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// validHeaderName reports whether s is an RFC 9110 field name (a token).
func validHeaderName(s string) bool {
	if len(s) > maxIdentityHeaderNameLength {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune(headerNameChars, r)
	}) < 0
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

// maxKeypairFieldLength bounds the account and user identifiers.
const maxKeypairFieldLength = 255

// NormalizeAuthType validates authType with its account/user pair. Empty defaults to bearer;
// keypair_jwt uppercases principal fields; azure_entra preserves the tenant and client IDs;
// wif accepts neither because the attestation names the principal.
func NormalizeAuthType(authType string, account, user *string) (string, *string, *string, error) {
	normalized := strings.ToLower(strings.TrimSpace(authType))
	// An account locator carries its region as dotted suffixes; the JWT claims
	// want the bare locator. Org-qualified identifiers are hyphenated and unaffected.
	upperAccount, _, _ := strings.Cut(strings.ToUpper(trimmedValue(account)), ".")
	upperUser := strings.ToUpper(trimmedValue(user))
	if normalized == "" {
		normalized = AuthTypeBearer
	}
	if normalized == AuthTypeAzureEntra {
		tenantID := strings.TrimSpace(trimmedValue(account))
		clientID := strings.TrimSpace(trimmedValue(user))
		if tenantID == "" || clientID == "" {
			return "", nil, nil, fmt.Errorf("%w: %s needs both a tenant ID and a client ID", ErrInvalidEntraAuth, AuthTypeAzureEntra)
		}
		if len(tenantID) > maxKeypairFieldLength || len(clientID) > maxKeypairFieldLength {
			return "", nil, nil, fmt.Errorf("%w: tenant ID and client ID are limited to %d characters", ErrInvalidEntraAuth, maxKeypairFieldLength)
		}
		return normalized, &tenantID, &clientID, nil
	}
	if normalized == AuthTypeBearer || normalized == AuthTypeWIF {
		if upperAccount != "" || upperUser != "" {
			return "", nil, nil, fmt.Errorf("%w: account and user apply to %s only", ErrInvalidKeypairAuth, AuthTypeKeypairJWT)
		}
		return normalized, nil, nil, nil
	}
	if normalized != AuthTypeKeypairJWT {
		return "", nil, nil, fmt.Errorf("%w: unknown auth type %q", ErrInvalidKeypairAuth, authType)
	}
	if upperAccount == "" || upperUser == "" {
		return "", nil, nil, fmt.Errorf("%w: %s needs both an account and a user", ErrInvalidKeypairAuth, AuthTypeKeypairJWT)
	}
	if len(upperAccount) > maxKeypairFieldLength || len(upperUser) > maxKeypairFieldLength {
		return "", nil, nil, fmt.Errorf("%w: account and user are limited to %d characters", ErrInvalidKeypairAuth, maxKeypairFieldLength)
	}
	return AuthTypeKeypairJWT, &upperAccount, &upperUser, nil
}

// trimmedValue returns the trimmed value behind s, or "" when s is nil.
func trimmedValue(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// ExternalAPIKeyRepository manages external API keys in storage.
type ExternalAPIKeyRepository interface {
	Create(ctx context.Context, params CreateExternalAPIKeyParams) (*ExternalAPIKey, error)
	// GetForInstallation returns all active keys with Plaintext populated.
	GetForInstallation(ctx context.Context, installationID string) ([]*ExternalAPIKey, error)
	SoftDeleteByProvider(ctx context.Context, installationID, provider string) error
	// UpdateModelAliases replaces one key's alias map, leaving the stored secret untouched.
	UpdateModelAliases(ctx context.Context, installationID, id string, aliases map[string]string) (*ExternalAPIKey, error)
	SoftDelete(ctx context.Context, installationID, id string) error
	MarkUsed(ctx context.Context, id string) error
}
