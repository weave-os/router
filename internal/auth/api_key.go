package auth

import (
	"context"
	"crypto/ed25519"
	"time"
)

// APIKeyScope is what a router-issued credential is allowed to do.
type APIKeyScope string

const (
	// ScopeRouting is the rk_ data-plane key: proxies inference, draws down
	// balance and spend caps.
	ScopeRouting APIKeyScope = "routing"
	// ScopeAnalyticsRead is the ra_ export key: reads the installation's own
	// telemetry and nothing else. It can neither route nor spend.
	ScopeAnalyticsRead APIKeyScope = "analytics_read"
	// ScopeSessionIssuer is a factory-registered Ed25519 public key. It is never
	// a bearer; it verifies rs_ session tokens and is what they authenticate as.
	ScopeSessionIssuer APIKeyScope = "session_issuer"
)

// Normalized resolves the zero value to ScopeRouting (mirrors the migration backfill);
// an unrecognized non-empty value passes through and fails every scope check.
func (s APIKeyScope) Normalized() APIKeyScope {
	if s == "" {
		return ScopeRouting
	}
	return s
}

// Valid reports whether the scope is one the router recognizes. Mirrors the
// CHECK constraint on model_router_api_keys.scope.
func (s APIKeyScope) Valid() bool {
	n := s.Normalized()
	return n == ScopeRouting || n == ScopeAnalyticsRead || n == ScopeSessionIssuer
}

// UserIssuable reports whether a user-facing API may mint a bearer key of this
// scope. Session issuers are registered only through the internal endpoint.
func (s APIKeyScope) UserIssuable() bool {
	n := s.Normalized()
	return n == ScopeRouting || n == ScopeAnalyticsRead
}

// TokenPrefix returns the raw-token prefix a key of this scope is issued under.
func (s APIKeyScope) TokenPrefix() string {
	if s == ScopeAnalyticsRead {
		return AnalyticsAPIKeyPrefix
	}
	return APIKeyPrefix
}

type APIKey struct {
	ID             string
	InstallationID string
	ExternalID     string
	Name           *string
	KeyPrefix      string
	KeyHash        string
	KeySuffix      string
	Scope          APIKeyScope
	LastUsedAt     *time.Time
	CreatedAt      time.Time
	DeletedAt      *time.Time
	CreatedBy      *string
	// CredentialSubjectID is authenticated ownership, unlike client-asserted attribution or CreatedBy.
	CredentialSubjectID string
	// Harness is the stored agent that minted this key (claude_code, codex, opencode, pi).
	Harness string
	// PaidFallbackOnSubscriptionRefusal is an admin opt-in that lets a forced
	// Claude turn use paid API capacity when every linked Claude subscription
	// refuses the client app. Never settable from a request.
	PaidFallbackOnSubscriptionRefusal bool
	// IssuerPublicKey is set only for ScopeSessionIssuer rows.
	IssuerPublicKey ed25519.PublicKey
}

type CreateAPIKeyParams struct {
	InstallationID string
	ExternalID     string
	Name           *string
	KeyPrefix      string
	KeyHash        string
	KeySuffix      string
	Scope          APIKeyScope
	CreatedBy      *string
	// PublicJWK is the canonical issuer JWK; required for, and only for, ScopeSessionIssuer.
	PublicJWK []byte
}

type APIKeyRepository interface {
	Create(ctx context.Context, params CreateAPIKeyParams) (*APIKey, error)
	GetActiveByHashWithInstallation(ctx context.Context, keyHash string) (*APIKey, *Installation, error)
	ListForInstallation(ctx context.Context, installationID string) ([]*APIKey, error)
	// MarkUsed stamps first use, coalesces later usage timestamps, and reports the first-use transition.
	MarkUsed(ctx context.Context, id string) (firstUse bool, err error)
	// SoftDelete soft-deletes the key and returns the rows-affected count; 0 means the key was already gone.
	SoftDelete(ctx context.Context, installationID, id string) (int64, error)
}
