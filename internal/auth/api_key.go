package auth

import (
	"context"
	"crypto/ed25519"
	"time"
)

type APIKeyScope string

const (
	ScopeRouting       APIKeyScope = "routing"
	ScopeAnalyticsRead APIKeyScope = "analytics_read"
	ScopeSessionIssuer APIKeyScope = "session_issuer"
)

func (s APIKeyScope) Normalized() APIKeyScope {
	if s == "" {
		return ScopeRouting
	}
	return s
}

func (s APIKeyScope) Valid() bool {
	n := s.Normalized()
	return n == ScopeRouting || n == ScopeAnalyticsRead || n == ScopeSessionIssuer
}

func (s APIKeyScope) UserIssuable() bool {
	n := s.Normalized()
	return n == ScopeRouting || n == ScopeAnalyticsRead
}

func (s APIKeyScope) TokenPrefix() string {
	if s == ScopeAnalyticsRead {
		return AnalyticsAPIKeyPrefix
	}
	return APIKeyPrefix
}

type APIKey struct {
	ID                                string
	InstallationID                    string
	ExternalID                        string
	Name                              *string
	KeyPrefix                         string
	KeyHash                           string
	KeySuffix                         string
	Scope                             APIKeyScope
	LastUsedAt                        *time.Time
	CreatedAt                         time.Time
	DeletedAt                         *time.Time
	CreatedBy                         *string
	CredentialSubjectID               string
	Harness                           string
	PaidFallbackOnSubscriptionRefusal bool
	IssuerPublicKey                   ed25519.PublicKey
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
	PublicJWK      []byte
}

type APIKeyRepository interface {
	Create(ctx context.Context, params CreateAPIKeyParams) (*APIKey, error)
	GetActiveByHashWithInstallation(ctx context.Context, keyHash string) (*APIKey, *Installation, error)
	ListForInstallation(ctx context.Context, installationID string) ([]*APIKey, error)
	MarkUsed(ctx context.Context, id string) (firstUse bool, err error)
	SoftDelete(ctx context.Context, installationID, id string) (int64, error)
}
