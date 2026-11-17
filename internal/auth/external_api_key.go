package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ExternalAPIKey struct {
	ID                     string
	InstallationID         string
	Provider               string
	Name                   *string
	KeyPrefix              string
	KeySuffix              string
	KeyFingerprint         string
	BaseURL                string
	ModelAliases           map[string]string
	IdentityHeader         string
	IdentityHeaderFormat   string
	ForwardedClientHeaders []string
	BaggageHeader          string
	AuthType               AuthType
	FederationConfig       *AnthropicFederationConfig
	CredentialError        error
	AuthAccount            string
	AuthUser               string
	FundingPriority        FundingPriority
	CreatedAt              time.Time
	LastUsedAt             *time.Time
	Plaintext              []byte
}

type CreateExternalAPIKeyParams struct {
	InstallationID         string
	ExternalID             string
	Provider               string
	KeyCiphertext          []byte
	KeyPrefix              string
	KeySuffix              string
	KeyFingerprint         string
	Name                   *string
	BaseURL                *string
	ModelAliases           map[string]string
	IdentityHeader         *string
	IdentityHeaderFormat   *string
	ForwardedClientHeaders []string
	BaggageHeader          *string
	FederationConfig       *AnthropicFederationConfig
	AuthType               AuthType
	AuthAccount            *string
	AuthUser               *string
	FundingPriority        FundingPriority
	CreatedBy              *string
}

type FundingPriority string

const (
	FundingPriorityKeyFirst  FundingPriority = "key_first"
	FundingPriorityPlanFirst FundingPriority = "plan_first"
)

var ErrInvalidFundingPriority = errors.New("funding priority must be key_first or plan_first")

func NormalizeFundingPriority(p FundingPriority) (FundingPriority, error) {
	switch p {
	case "", FundingPriorityPlanFirst:
		return FundingPriorityPlanFirst, nil
	case FundingPriorityKeyFirst:
		return FundingPriorityKeyFirst, nil
	}
	return "", ErrInvalidFundingPriority
}

type AuthType string

const (
	AuthTypeBearer       AuthType = "bearer"
	AuthTypeKeypairJWT   AuthType = "keypair_jwt"
	AuthTypeAzureEntra   AuthType = "azure_entra"
	AuthTypeWIF          AuthType = "wif"
	AuthTypeAnthropicWIF AuthType = "anthropic_wif"
)

const (
	IdentityFormatEmail = "email"
	IdentityFormatJSON  = "json"
)

const maxIdentityHeaderNameLength = 128

const (
	weaveHeaderPrefix     = "x-weave-"
	forwardedHeaderPrefix = "x-forwarded-"
)

var protectedForwardingHeaders = map[string]struct{}{
	"accept":                               {},
	"accept-encoding":                      {},
	"anthropic-beta":                       {},
	"anthropic-version":                    {},
	"api-key":                              {},
	"authentication":                       {},
	"authentication-info":                  {},
	"authorization":                        {},
	"chatgpt-account-id":                   {},
	"connection":                           {},
	"content-encoding":                     {},
	"content-length":                       {},
	"content-type":                         {},
	"cookie":                               {},
	"forwarded":                            {},
	"host":                                 {},
	"keep-alive":                           {},
	"metadata":                             {},
	"metadata-flavor":                      {},
	"ocp-apim-subscription-key":            {},
	"openai-organization":                  {},
	"openai-project":                       {},
	"proxy-authenticate":                   {},
	"proxy-authorization":                  {},
	"proxy-connection":                     {},
	"set-cookie":                           {},
	"te":                                   {},
	"trailer":                              {},
	"transfer-encoding":                    {},
	"upgrade":                              {},
	"www-authenticate":                     {},
	"x-access-token":                       {},
	"x-amz-content-sha256":                 {},
	"x-amz-date":                           {},
	"x-amz-security-token":                 {},
	"x-amz-target":                         {},
	"x-api-key":                            {},
	"x-auth-token":                         {},
	"x-authentication":                     {},
	"x-aws-ec2-metadata-token":             {},
	"x-aws-ec2-metadata-token-ttl-seconds": {},
	"x-goog-api-key":                       {},
	"x-goog-metadata-request":              {},
	"x-real-ip":                            {},
	"x-original-url":                       {},
	"x-rewrite-url":                        {},
}

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
	if !IsSafeForwardingHeader(trimmedName) {
		return nil, nil, fmt.Errorf("%w: %q is reserved", ErrInvalidIdentityHeader, trimmedName)
	}
	if trimmedFormat != IdentityFormatEmail && trimmedFormat != IdentityFormatJSON {
		return nil, nil, fmt.Errorf("%w: unknown format %q", ErrInvalidIdentityHeader, trimmedFormat)
	}
	return &trimmedName, &trimmedFormat, nil
}

const maxForwardedClientHeaders = 16

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

func ValidateForwardingHeaderConfiguration(forwarded []string, baggage, identity string) error {
	seen := make(map[string]struct{}, len(forwarded)+2)
	for _, name := range forwarded {
		lowerName := strings.ToLower(name)
		if _, duplicate := seen[lowerName]; duplicate {
			return fmt.Errorf("%w: header destinations must be distinct", ErrInvalidForwardedHeader)
		}
		seen[lowerName] = struct{}{}
	}
	for _, name := range []string{baggage, identity} {
		if name == "" {
			continue
		}
		lowerName := strings.ToLower(name)
		if _, collision := seen[lowerName]; collision {
			return fmt.Errorf("%w: header destinations must be distinct", ErrInvalidForwardedHeader)
		}
		seen[lowerName] = struct{}{}
	}
	return nil
}

func validateForwardableHeader(name string) error {
	if !IsSafeForwardingHeader(name) {
		if !validHeaderName(name) {
			return fmt.Errorf("%w: %q is not a valid header name", ErrInvalidForwardedHeader, name)
		}
		return fmt.Errorf("%w: %q is reserved", ErrInvalidForwardedHeader, name)
	}
	return nil
}

func IsSafeForwardingHeader(name string) bool {
	if name == "" || strings.TrimSpace(name) != name || !validHeaderName(name) {
		return false
	}
	lowerName := strings.ToLower(name)
	if strings.HasPrefix(lowerName, weaveHeaderPrefix) || strings.HasPrefix(lowerName, forwardedHeaderPrefix) {
		return false
	}
	if _, protected := protectedForwardingHeaders[lowerName]; protected {
		return false
	}
	return true
}

const headerNameChars = "!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func validHeaderName(s string) bool {
	if len(s) > maxIdentityHeaderNameLength {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune(headerNameChars, r)
	}) < 0
}

const maxModelAliases = 256

const maxModelAliasLength = 255

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

func NormalizeBaseURL(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimRight(strings.TrimSpace(*raw), "/")
	if trimmed == "" {
		return nil, nil
	}
	if strings.ContainsAny(trimmed, "?#") {
		return nil, ErrInvalidBaseURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, ErrInvalidBaseURL
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		!validBaseURLPort(parsed) {
		return nil, ErrInvalidBaseURL
	}
	return &trimmed, nil
}

func validBaseURLPort(parsed *url.URL) bool {
	if strings.HasSuffix(parsed.Host, ":") {
		return false
	}
	port := parsed.Port()
	if port == "" {
		return true
	}
	portNumber, err := strconv.Atoi(port)
	return err == nil && portNumber >= 1 && portNumber <= 65535
}

const maxKeypairFieldLength = 255

func NormalizeAuthType(authType AuthType, account, user *string) (AuthType, *string, *string, error) {
	normalized := AuthType(strings.ToLower(strings.TrimSpace(string(authType))))
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
	if normalized == AuthTypeBearer || normalized == AuthTypeWIF || normalized == AuthTypeAnthropicWIF {
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

func trimmedValue(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

type ExternalAPIKeyRepository interface {
	Create(ctx context.Context, params CreateExternalAPIKeyParams) (*ExternalAPIKey, error)
	GetForInstallation(ctx context.Context, installationID string) ([]*ExternalAPIKey, error)
	SoftDeleteByProvider(ctx context.Context, installationID, provider string) error
	UpdateModelAliases(ctx context.Context, installationID, id string, aliases map[string]string) (*ExternalAPIKey, error)
	UpdateFundingPriority(ctx context.Context, installationID, id string, priority FundingPriority) (*ExternalAPIKey, error)
	ActiveFundingPriority(ctx context.Context, installationID, provider string) (FundingPriority, error)
	SoftDelete(ctx context.Context, installationID, id string) error
	MarkUsed(ctx context.Context, id string) error
}
