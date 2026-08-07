package auth

import (
	"context"
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
}

type APIKeyRepository interface {
	Create(ctx context.Context, params CreateAPIKeyParams) (*APIKey, error)
	GetActiveByHashWithInstallation(ctx context.Context, keyHash string) (*APIKey, *Installation, error)
	ListForInstallation(ctx context.Context, installationID string) ([]*APIKey, error)
	MarkUsed(ctx context.Context, id string) error
	// SoftDelete soft-deletes the key and returns the rows-affected count; 0 means the key was already gone.
	SoftDelete(ctx context.Context, installationID, id string) (int64, error)
}
