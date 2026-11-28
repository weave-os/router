package auth

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"weave-os/router/internal/observability"

	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
)

const (
	SessionTokenIssuer      = "session-issuer"
	SessionTokenAudience    = "weave-router"
	SessionTokenType        = "at+jwt"
	SessionTokenMaxLifetime = 2 * time.Hour
	SessionTokenLeeway      = 30 * time.Second
	SessionTokenMaxBytes    = 4096
	SessionTokenMaxIDLength = 255

	sessionIssuerKeyHashPrefix = "jwk:"
)

var ErrInvalidSessionIssuerKey = errors.New("session issuer key must be an Ed25519 public JWK")

var ErrSessionIssuerConflict = errors.New("session issuer key is registered to another installation")

type SessionCaps struct {
	SessionUSDMicros *int64 `json:"session_usd_micros,omitempty"`
	TaskUSDMicros    *int64 `json:"task_usd_micros,omitempty"`
}

type SessionClaims struct {
	jwt.RegisteredClaims
	IssuerID string       `json:"https://weave.ai/issuer_id"`
	Task     string       `json:"https://weave.ai/task,omitempty"`
	Caps     *SessionCaps `json:"https://weave.ai/caps,omitempty"`
}

type DelegatedSession struct {
	IssuerKeyID      string
	SessionID        string
	TaskID           string
	SessionCapMicros *int64
	TaskCapMicros    *int64
	ExpiresAt        time.Time
}

type delegatedSessionContextKey struct{}

func WithDelegatedSession(ctx context.Context, session *DelegatedSession) context.Context {
	if session == nil {
		return ctx
	}
	return context.WithValue(ctx, delegatedSessionContextKey{}, session)
}

func DelegatedSessionFrom(ctx context.Context) *DelegatedSession {
	session, _ := ctx.Value(delegatedSessionContextKey{}).(*DelegatedSession)
	return session
}

func IsSessionToken(token string) bool {
	return strings.HasPrefix(token, SessionTokenPrefix+"_")
}

func ParseSessionIssuerJWK(raw []byte) (ed25519.PublicKey, string, []byte, error) {
	var jwk jose.JSONWebKey
	if err := jwk.UnmarshalJSON(raw); err != nil {
		return nil, "", nil, fmt.Errorf("%w: %w", ErrInvalidSessionIssuerKey, err)
	}
	publicKey, ok := jwk.Key.(ed25519.PublicKey)
	if !ok || !jwk.Valid() || len(publicKey) != ed25519.PublicKeySize {
		return nil, "", nil, ErrInvalidSessionIssuerKey
	}
	thumbprint, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, "", nil, fmt.Errorf("%w: %w", ErrInvalidSessionIssuerKey, err)
	}
	canonical, err := jose.JSONWebKey{Key: publicKey}.MarshalJSON()
	if err != nil {
		return nil, "", nil, err
	}
	return publicKey, base64.RawURLEncoding.EncodeToString(thumbprint), canonical, nil
}

func SessionIssuerKeyHash(kid string) string {
	return sessionIssuerKeyHashPrefix + kid
}

func (s *Service) WithSessionSpendCaps(enabled bool) *Service {
	s.delegatedSpendCapsEnabled = enabled
	return s
}

func (s *Service) RegisterSessionIssuer(ctx context.Context, installationID string, name *string, publicJWK []byte) (*APIKey, string, error) {
	_, kid, canonical, err := ParseSessionIssuerJWK(publicJWK)
	if err != nil {
		return nil, "", err
	}
	keyHash := SessionIssuerKeyHash(kid)
	existing, err := s.existingSessionIssuer(ctx, installationID, keyHash)
	if existing != nil || !errors.Is(err, sql.ErrNoRows) {
		return existing, kid, err
	}
	key, err := s.apiKeys.Create(ctx, CreateAPIKeyParams{
		InstallationID: installationID,
		ExternalID:     GenerateID("kid"),
		Name:           name,
		KeyPrefix:      keyHash[:len(sessionIssuerKeyHashPrefix)+8],
		KeyHash:        keyHash,
		KeySuffix:      kid[len(kid)-APITokenSuffixLen:],
		Scope:          ScopeSessionIssuer,
		PublicJWK:      canonical,
	})
	if err != nil {
		if winner, lookupErr := s.existingSessionIssuer(ctx, installationID, keyHash); winner != nil || errors.Is(lookupErr, ErrSessionIssuerConflict) {
			return winner, kid, lookupErr
		}
		return nil, "", err
	}
	return key, kid, nil
}

func (s *Service) existingSessionIssuer(ctx context.Context, installationID, keyHash string) (*APIKey, error) {
	existing, _, err := s.apiKeys.GetActiveByHashWithInstallation(ctx, keyHash)
	if err != nil {
		return nil, err
	}
	if existing.InstallationID != installationID || existing.Scope != ScopeSessionIssuer {
		return nil, ErrSessionIssuerConflict
	}
	return existing, nil
}

func (s *Service) RevokeSessionIssuer(ctx context.Context, installationID, kid string) error {
	key, _, err := s.apiKeys.GetActiveByHashWithInstallation(ctx, SessionIssuerKeyHash(kid))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAPIKeyNotFound
	}
	if err != nil {
		return err
	}
	if key.InstallationID != installationID || key.Scope != ScopeSessionIssuer {
		return ErrAPIKeyNotFound
	}
	return s.DeleteAPIKey(ctx, installationID, key.ID)
}

func (s *Service) VerifySessionToken(ctx context.Context, rawToken string, includeUpstreamKeys bool) (*Installation, *APIKey, []*ExternalAPIKey, []ClusterModelList, *DelegatedSession, error) {
	if !IsSessionToken(rawToken) {
		return nil, nil, nil, nil, nil, ErrInvalidPrefix
	}
	if len(rawToken) > SessionTokenMaxBytes {
		return nil, nil, nil, nil, nil, ErrInvalidToken
	}
	var issuer CachedKey
	var lookupErr error
	claims := &SessionClaims{}
	token, err := jwt.ParseWithClaims(
		strings.TrimPrefix(rawToken, SessionTokenPrefix+"_"),
		claims,
		func(token *jwt.Token) (any, error) {
			typ, _ := token.Header["typ"].(string)
			if typ = strings.ToLower(typ); typ != SessionTokenType && typ != "application/"+SessionTokenType {
				return nil, ErrInvalidToken
			}
			kid, _ := token.Header["kid"].(string)
			if kid == "" {
				return nil, ErrInvalidToken
			}
			issuer, lookupErr = s.sessionIssuer(ctx, kid, includeUpstreamKeys)
			if lookupErr != nil {
				return nil, lookupErr
			}
			return issuer.APIKey.IssuerPublicKey, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(SessionTokenIssuer),
		jwt.WithAudience(SessionTokenAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(SessionTokenLeeway),
		jwt.WithTimeFunc(s.now),
	)
	if lookupErr != nil && !errors.Is(lookupErr, ErrInvalidToken) {
		return nil, nil, nil, nil, nil, lookupErr
	}
	if err != nil || !token.Valid {
		return nil, nil, nil, nil, nil, ErrInvalidToken
	}
	if claims.IssuerID != issuer.APIKey.ID || claims.Subject == "" || len(claims.Subject) > SessionTokenMaxIDLength || len(claims.Task) > SessionTokenMaxIDLength ||
		strings.ContainsRune(claims.Subject, '\x00') || strings.ContainsRune(claims.Task, '\x00') || claims.IssuedAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > SessionTokenMaxLifetime {
		return nil, nil, nil, nil, nil, ErrInvalidToken
	}
	session := &DelegatedSession{
		IssuerKeyID: issuer.APIKey.ID,
		SessionID:   claims.Subject,
		TaskID:      claims.Task,
		ExpiresAt:   claims.ExpiresAt.UTC(),
	}
	if claims.Caps != nil {
		if !s.delegatedSpendCapsEnabled {
			return nil, nil, nil, nil, nil, ErrInvalidToken
		}
		noCap := claims.Caps.SessionUSDMicros == nil && claims.Caps.TaskUSDMicros == nil
		if noCap || !validCap(claims.Caps.SessionUSDMicros) || !validCap(claims.Caps.TaskUSDMicros) || (claims.Caps.TaskUSDMicros != nil && claims.Task == "") {
			return nil, nil, nil, nil, nil, ErrInvalidToken
		}
		session.SessionCapMicros, session.TaskCapMicros = claims.Caps.SessionUSDMicros, claims.Caps.TaskUSDMicros
	}
	s.fireMarkUsed(ctx, issuer.APIKey, issuer.Installation)
	s.fireMarkFirstRequestServed(ctx, issuer.APIKey.InstallationID, issuer.Installation)
	if !includeUpstreamKeys {
		return issuer.Installation, issuer.APIKey, nil, issuer.ClusterModelLists, session, nil
	}
	return issuer.Installation, issuer.APIKey, s.resolveUpstreamSecrets(ctx, issuer.ExternalKeys), issuer.ClusterModelLists, session, nil
}

func validCap(capMicros *int64) bool {
	return capMicros == nil || *capMicros > 0
}

func (s *Service) sessionIssuer(ctx context.Context, kid string, includeUpstreamKeys bool) (CachedKey, error) {
	cacheKey := SessionIssuerKeyHash(kid)
	if cached, ok := s.cache.Get(cacheKey); ok {
		return cached, nil
	}
	apiKey, installation, err := s.apiKeys.GetActiveByHashWithInstallation(ctx, cacheKey)
	if errors.Is(err, sql.ErrNoRows) {
		return CachedKey{}, ErrInvalidToken
	}
	if err != nil {
		return CachedKey{}, err
	}
	if apiKey.Scope != ScopeSessionIssuer || len(apiKey.IssuerPublicKey) != ed25519.PublicKeySize {
		return CachedKey{}, ErrInvalidToken
	}
	entry := CachedKey{APIKey: apiKey, Installation: installation}
	if includeUpstreamKeys && s.externalKeys != nil {
		entry.ExternalKeys, err = s.externalKeys.GetForInstallation(ctx, apiKey.InstallationID)
		if err != nil {
			observability.FromContext(ctx).Warn("Failed to fetch external API keys", "installation_id", apiKey.InstallationID, "err", err)
		}
	}
	cacheable := includeUpstreamKeys
	if s.clusterModelLists != nil {
		entry.ClusterModelLists, err = s.clusterModelLists.GetForAPIKey(ctx, apiKey.ID)
		if err != nil {
			observability.FromContext(ctx).Warn("Failed to fetch cluster model lists", "api_key_id", apiKey.ID, "err", err)
			entry.ClusterModelLists = nil
			cacheable = false
		}
	}
	if cacheable {
		s.cache.Set(cacheKey, entry)
	}
	return entry, nil
}
