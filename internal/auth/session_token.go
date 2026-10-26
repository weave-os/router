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

// Delegated session tokens are RFC 9068 JWT access tokens signed with Ed25519
// (RFC 8037) by an issuer-held key whose public JWK is registered as a
// session_issuer row. Validation follows RFC 8725.
const (
	SessionTokenIssuer      = "session-issuer"
	SessionTokenAudience    = "weave-router"
	SessionTokenType        = "at+jwt"
	SessionTokenMaxLifetime = 24 * time.Hour
	SessionTokenLeeway      = 30 * time.Second
	// SessionTokenMaxBytes bounds parsing work for an unauthenticated input.
	SessionTokenMaxBytes = 4096
	// SessionTokenMaxIDLength matches router.delegated_token_spend.scope_id, so a verified
	// session or task ID can always be billed.
	SessionTokenMaxIDLength = 255

	sessionIssuerKeyHashPrefix = "jwk:"
)

// ErrInvalidSessionIssuerKey is returned when registering a JWK that is not an Ed25519 public key.
var ErrInvalidSessionIssuerKey = errors.New("session issuer key must be an Ed25519 public JWK")

// ErrSessionIssuerConflict is returned when a public key is already registered to another installation.
var ErrSessionIssuerConflict = errors.New("session issuer key is registered to another installation")

// SessionCaps are optional USD spend limits a session token carries. A task
// cap requires the task claim; both caps must be positive.
type SessionCaps struct {
	SessionUSDMicros *int64 `json:"session_usd_micros,omitempty"`
	TaskUSDMicros    *int64 `json:"task_usd_micros,omitempty"`
}

// SessionClaims is the JWT claim set of a delegated session token. Private
// claims use collision-resistant names (RFC 7519 §4.3).
type SessionClaims struct {
	jwt.RegisteredClaims
	Task string       `json:"https://weave.ai/task,omitempty"`
	Caps *SessionCaps `json:"https://weave.ai/caps,omitempty"`
}

// DelegatedSession is the verified identity of a session token. Requests
// authenticate as the issuer key; this narrows them to one session and task.
type DelegatedSession struct {
	IssuerKeyID      string
	SessionID        string
	TaskID           string
	SessionCapMicros *int64
	TaskCapMicros    *int64
	ExpiresAt        time.Time
}

type delegatedSessionContextKey struct{}

// WithDelegatedSession stores the verified session on ctx; a nil session leaves ctx unchanged.
func WithDelegatedSession(ctx context.Context, session *DelegatedSession) context.Context {
	if session == nil {
		return ctx
	}
	return context.WithValue(ctx, delegatedSessionContextKey{}, session)
}

// DelegatedSessionFrom returns the verified session, or nil for any other credential.
func DelegatedSessionFrom(ctx context.Context) *DelegatedSession {
	session, _ := ctx.Value(delegatedSessionContextKey{}).(*DelegatedSession)
	return session
}

func IsSessionToken(token string) bool {
	return strings.HasPrefix(token, SessionTokenPrefix+"_")
}

// ParseSessionIssuerJWK accepts only an OKP Ed25519 public JWK and returns the
// key, its RFC 7638 SHA-256 thumbprint (base64url) and the canonical public JWK.
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

// SessionIssuerKeyHash is the key_hash an issuer row is stored and looked up under.
// It can never equal a hex SHA-256 bearer hash, so an issuer is unreachable as a bearer.
func SessionIssuerKeyHash(kid string) string {
	return sessionIssuerKeyHashPrefix + kid
}

func (s *Service) WithSessionTokens(enabled bool) *Service {
	s.sessionTokensEnabled = enabled
	return s
}

// SessionTokensEnabled reports whether rs_ session tokens are accepted; a nil Service reports false.
func (s *Service) SessionTokensEnabled() bool {
	return s != nil && s.sessionTokensEnabled
}

// RegisterSessionIssuer stores an Ed25519 public JWK as a session issuer for
// the installation and returns its kid. Registering the same key again is a no-op.
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
		// A concurrent registration of the same key wins the unique key_hash index.
		if winner, lookupErr := s.existingSessionIssuer(ctx, installationID, keyHash); winner != nil || errors.Is(lookupErr, ErrSessionIssuerConflict) {
			return winner, kid, lookupErr
		}
		return nil, "", err
	}
	return key, kid, nil
}

// existingSessionIssuer returns the active issuer stored under keyHash, sql.ErrNoRows
// when there is none, or ErrSessionIssuerConflict when another installation owns it.
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

// RevokeSessionIssuer soft-deletes the issuer, which revokes every session
// token it signed once the installation's cache entries are invalidated.
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

// VerifySessionToken authenticates an rs_ session token as its issuer key and
// returns the delegated session. The only lookup is the cached issuer row, so
// many concurrent sessions share one cache entry per issuer.
func (s *Service) VerifySessionToken(ctx context.Context, rawToken string, includeUpstreamKeys bool) (*Installation, *APIKey, []*ExternalAPIKey, []ClusterModelList, *DelegatedSession, error) {
	if !IsSessionToken(rawToken) {
		return nil, nil, nil, nil, nil, ErrInvalidPrefix
	}
	if !s.sessionTokensEnabled || len(rawToken) > SessionTokenMaxBytes {
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
	if claims.Subject == "" || len(claims.Subject) > SessionTokenMaxIDLength || len(claims.Task) > SessionTokenMaxIDLength ||
		claims.IssuedAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > SessionTokenMaxLifetime {
		return nil, nil, nil, nil, nil, ErrInvalidToken
	}
	session := &DelegatedSession{
		IssuerKeyID: issuer.APIKey.ID,
		SessionID:   claims.Subject,
		TaskID:      claims.Task,
		ExpiresAt:   claims.ExpiresAt.UTC(),
	}
	if claims.Caps != nil {
		// An empty or misspelled caps object is refused so the issuer's mistake surfaces
		// instead of minting an uncapped credential.
		noCap := claims.Caps.SessionUSDMicros == nil && claims.Caps.TaskUSDMicros == nil
		if noCap || !validCap(claims.Caps.SessionUSDMicros) || !validCap(claims.Caps.TaskUSDMicros) || (claims.Caps.TaskUSDMicros != nil && claims.Task == "") {
			return nil, nil, nil, nil, nil, ErrInvalidToken
		}
		session.SessionCapMicros, session.TaskCapMicros = claims.Caps.SessionUSDMicros, claims.Caps.TaskUSDMicros
	}
	s.fireMarkUsed(issuer.APIKey, issuer.Installation)
	s.fireMarkFirstRequestServed(issuer.APIKey.InstallationID)
	if !includeUpstreamKeys {
		return issuer.Installation, issuer.APIKey, nil, issuer.ClusterModelLists, session, nil
	}
	return issuer.Installation, issuer.APIKey, s.resolveUpstreamSecrets(ctx, issuer.ExternalKeys), issuer.ClusterModelLists, session, nil
}

func validCap(capMicros *int64) bool {
	return capMicros == nil || *capMicros > 0
}

// sessionIssuer resolves an active issuer by kid through the shared auth cache.
// Positive entries are evicted with the installation, so revocation takes effect
// on the same path as deleting any other key. An unknown kid is not negatively
// cached: registration could not evict it on every replica, so a token signed
// right after its issuer is registered would 401 for the negative TTL.
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
			// Same rule as rk_ keys: serve with default routing but never cache a
			// transient miss of the issuer's cluster restrictions.
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
