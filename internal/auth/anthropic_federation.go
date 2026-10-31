package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"weave-os/router/internal/providers"
)

// AnthropicFederationConfig identifies the trust rule and workspace that authorize a workload.
// These identifiers are configuration, not credentials.
type AnthropicFederationConfig struct {
	FederationRuleID string `json:"federation_rule_id"`
	OrganizationID   string `json:"organization_id"`
	ServiceAccountID string `json:"service_account_id"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
	Audience         string `json:"audience"`
}

// AnthropicFederationAudience is the default audience and the fixed API origin.
const AnthropicFederationAudience = "https://api.anthropic.com"

// AnthropicBeta names protocol flags required for OAuth and federation requests.
type AnthropicBeta string

const (
	AnthropicBetaOAuth      AnthropicBeta = "oauth-2025-04-20"
	AnthropicBetaFederation AnthropicBeta = "oidc-federation-2026-04-01"
)

// ErrInvalidAnthropicFederation marks rejected provider credential configuration.
var ErrInvalidAnthropicFederation = errors.New("invalid Anthropic workload federation configuration")

// ErrAnthropicFederationUnavailable prevents fallback when configured federation fails.
var ErrAnthropicFederationUnavailable = errors.New("Anthropic workload federation is unavailable")

// NormalizeAnthropicFederation prevents configuration from being silently ignored or
// an exchanged token from being sent to a customer-controlled endpoint.
func NormalizeAnthropicFederation(authType AuthType, provider, baseURL string, federation *AnthropicFederationConfig) (*AnthropicFederationConfig, error) {
	if authType != AuthTypeAnthropicWIF {
		if federation != nil {
			return nil, fmt.Errorf("%w: federation settings require Anthropic workload identity auth", ErrInvalidAnthropicFederation)
		}
		return nil, nil
	}
	if provider != providers.ProviderAnthropic || (baseURL != "" && strings.TrimRight(baseURL, "/") != AnthropicFederationAudience) {
		return nil, fmt.Errorf("%w: use the native Anthropic provider and its default API endpoint", ErrInvalidAnthropicFederation)
	}
	if federation == nil {
		return nil, fmt.Errorf("%w: federation settings are required", ErrInvalidAnthropicFederation)
	}
	normalized := *federation
	normalized.FederationRuleID = strings.TrimSpace(normalized.FederationRuleID)
	normalized.OrganizationID = strings.TrimSpace(normalized.OrganizationID)
	normalized.ServiceAccountID = strings.TrimSpace(normalized.ServiceAccountID)
	normalized.WorkspaceID = strings.TrimSpace(normalized.WorkspaceID)
	normalized.Audience = strings.TrimSpace(normalized.Audience)
	if normalized.Audience == "" {
		normalized.Audience = AnthropicFederationAudience
	}
	for prefix, identifier := range map[string]string{"fdrl_": normalized.FederationRuleID, "svac_": normalized.ServiceAccountID} {
		if !strings.HasPrefix(identifier, prefix) || len(identifier) <= len(prefix) || len(identifier) > 255 || strings.ContainsAny(identifier, " \t\r\n") {
			return nil, fmt.Errorf("%w: expected a valid %s identifier", ErrInvalidAnthropicFederation, prefix)
		}
	}
	organizationID, err := uuid.Parse(normalized.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("%w: organization ID must be a UUID", ErrInvalidAnthropicFederation)
	}
	normalized.OrganizationID = organizationID.String()
	if normalized.WorkspaceID != "" && (!strings.HasPrefix(normalized.WorkspaceID, "wrkspc_") || len(normalized.WorkspaceID) <= len("wrkspc_") || len(normalized.WorkspaceID) > 255 || strings.ContainsAny(normalized.WorkspaceID, " \t\r\n")) {
		return nil, fmt.Errorf("%w: expected a wrkspc_ workspace identifier", ErrInvalidAnthropicFederation)
	}
	if len(normalized.Audience) > 2048 || strings.ContainsAny(normalized.Audience, "\r\n") {
		return nil, fmt.Errorf("%w: audience must be at most 2048 characters without line breaks", ErrInvalidAnthropicFederation)
	}
	return &normalized, nil
}

// AnthropicFederationTokenSource exchanges an identity assertion for an API bearer.
// It must isolate cached tokens by installation and federation target.
type AnthropicFederationTokenSource interface {
	Token(context.Context, *ExternalAPIKey) ([]byte, error)
}
