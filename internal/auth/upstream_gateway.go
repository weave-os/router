package auth

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	UpstreamGatewayTokenMaxBytes      = 8192
	DefaultUpstreamGatewayMaxLifetime = time.Hour
	upstreamGatewayLeeway             = 30 * time.Second
)

var DefaultUpstreamGatewayAlgorithms = []string{"RS256", "ES256"}

var upstreamGatewayAlgorithms = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}

var ErrInvalidUpstreamGatewayIssuer = errors.New("auth: invalid upstream gateway issuer config")

type UpstreamGatewayIssuer struct {
	IssuerURL        string
	Audience         string
	Subjects         []string
	Algorithms       []string
	MaxLifetime      time.Duration
	SessionIssuerKID string
	SessionHeaders   []string
}

type UpstreamGatewayPrincipal struct {
	IssuerURL      string
	Subject        string
	SessionID      string
	SessionHeaders []string
}

type OIDCKeySet interface {
	VerificationKey(ctx context.Context, issuerURL, kid string) (crypto.PublicKey, error)
}

type upstreamGatewayIssuerJSON struct {
	IssuerURL        string   `json:"issuer_url"`
	Audience         string   `json:"audience"`
	Subjects         []string `json:"subjects"`
	Algorithms       []string `json:"algorithms"`
	MaxLifetime      string   `json:"max_lifetime"`
	SessionIssuerKID string   `json:"session_issuer_kid"`
	SessionHeaders   []string `json:"session_headers"`
}

func ParseUpstreamGatewayIssuers(raw string) ([]UpstreamGatewayIssuer, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var entries []upstreamGatewayIssuerJSON
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidUpstreamGatewayIssuer, err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("%w: unexpected data after the issuer list", ErrInvalidUpstreamGatewayIssuer)
	}
	issuers := make([]UpstreamGatewayIssuer, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		issuer, err := entry.validate()
		if err != nil {
			return nil, err
		}
		if _, dup := seen[issuer.IssuerURL]; dup {
			return nil, fmt.Errorf("%w: issuer %q is configured twice", ErrInvalidUpstreamGatewayIssuer, issuer.IssuerURL)
		}
		seen[issuer.IssuerURL] = struct{}{}
		issuers = append(issuers, issuer)
	}
	return issuers, nil
}

func (e upstreamGatewayIssuerJSON) validate() (UpstreamGatewayIssuer, error) {
	invalid := func(format string, args ...any) (UpstreamGatewayIssuer, error) {
		return UpstreamGatewayIssuer{}, fmt.Errorf("%w: "+format, append([]any{ErrInvalidUpstreamGatewayIssuer}, args...)...)
	}
	parsed, err := url.Parse(e.IssuerURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return invalid("issuer_url %q must be an https URL", e.IssuerURL)
	}
	if strings.TrimSpace(e.Audience) == "" {
		return invalid("issuer %q needs an audience", e.IssuerURL)
	}
	if len(e.Subjects) == 0 || slices.Contains(e.Subjects, "") {
		return invalid("issuer %q needs non-empty allowed subjects", e.IssuerURL)
	}
	if strings.TrimSpace(e.SessionIssuerKID) == "" {
		return invalid("issuer %q needs a session_issuer_kid", e.IssuerURL)
	}
	algorithms := e.Algorithms
	if len(algorithms) == 0 {
		algorithms = DefaultUpstreamGatewayAlgorithms
	}
	for _, alg := range algorithms {
		if !slices.Contains(upstreamGatewayAlgorithms, alg) {
			return invalid("issuer %q algorithm %q is not an allowed asymmetric algorithm", e.IssuerURL, alg)
		}
	}
	for _, name := range e.SessionHeaders {
		if !validHeaderName(name) {
			return invalid("issuer %q session header %q is not a valid header name", e.IssuerURL, name)
		}
	}
	maxLifetime := DefaultUpstreamGatewayMaxLifetime
	if e.MaxLifetime != "" {
		maxLifetime, err = time.ParseDuration(e.MaxLifetime)
		if err != nil || maxLifetime <= 0 {
			return invalid("issuer %q max_lifetime %q must be a positive duration", e.IssuerURL, e.MaxLifetime)
		}
	}
	return UpstreamGatewayIssuer{
		IssuerURL:        e.IssuerURL,
		Audience:         e.Audience,
		Subjects:         slices.Clone(e.Subjects),
		Algorithms:       slices.Clone(algorithms),
		MaxLifetime:      maxLifetime,
		SessionIssuerKID: e.SessionIssuerKID,
		SessionHeaders:   slices.Clone(e.SessionHeaders),
	}, nil
}

type upstreamGatewayVerifier struct {
	issuers []UpstreamGatewayIssuer
	keys    OIDCKeySet
}

func (s *Service) WithUpstreamGatewayIssuers(issuers []UpstreamGatewayIssuer, keys OIDCKeySet) *Service {
	s.upstreamGateway = upstreamGatewayVerifier{issuers: issuers, keys: keys}
	return s
}

func IsUpstreamGatewayToken(token string) bool {
	return strings.HasPrefix(token, "eyJ") && strings.Count(token, ".") == 2
}

func (i UpstreamGatewayIssuer) maxLifetime() time.Duration {
	if i.MaxLifetime <= 0 {
		return DefaultUpstreamGatewayMaxLifetime
	}
	return i.MaxLifetime
}

func (i UpstreamGatewayIssuer) algorithms() []string {
	if len(i.Algorithms) == 0 {
		return DefaultUpstreamGatewayAlgorithms
	}
	return i.Algorithms
}

func (s *Service) upstreamGatewayIssuer(iss string) (UpstreamGatewayIssuer, bool) {
	for _, issuer := range s.upstreamGateway.issuers {
		if issuer.IssuerURL == iss {
			return issuer, true
		}
	}
	return UpstreamGatewayIssuer{}, false
}

func (s *Service) VerifyUpstreamGatewayToken(ctx context.Context, rawToken string) (*Installation, *APIKey, []*ExternalAPIKey, []ClusterModelList, *UpstreamGatewayPrincipal, error) {
	if s.upstreamGateway.keys == nil || !IsUpstreamGatewayToken(rawToken) || len(rawToken) > UpstreamGatewayTokenMaxBytes {
		return nil, nil, nil, nil, nil, ErrInvalidToken
	}
	unverified := &jwt.RegisteredClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(rawToken, unverified); err != nil {
		return nil, nil, nil, nil, nil, ErrInvalidToken
	}
	issuer, ok := s.upstreamGatewayIssuer(unverified.Issuer)
	if !ok {
		return nil, nil, nil, nil, nil, ErrInvalidToken
	}
	claims, err := s.verifyUpstreamGatewayClaims(ctx, rawToken, issuer)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	bound, err := s.sessionIssuer(ctx, issuer.SessionIssuerKID, true)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	principal := &UpstreamGatewayPrincipal{
		IssuerURL:      issuer.IssuerURL,
		Subject:        claims.Subject,
		SessionHeaders: issuer.SessionHeaders,
	}
	s.fireMarkUsed(ctx, bound.APIKey, bound.Installation)
	s.fireMarkFirstRequestServed(ctx, bound.APIKey.InstallationID)
	return bound.Installation, bound.APIKey, s.resolveUpstreamSecrets(ctx, bound.ExternalKeys), bound.ClusterModelLists, principal, nil
}

func (s *Service) verifyUpstreamGatewayClaims(ctx context.Context, rawToken string, issuer UpstreamGatewayIssuer) (*jwt.RegisteredClaims, error) {
	var lookupErr error
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(rawToken, claims,
		func(token *jwt.Token) (any, error) {
			kid, _ := token.Header["kid"].(string)
			if kid == "" {
				return nil, ErrInvalidToken
			}
			var key crypto.PublicKey
			key, lookupErr = s.upstreamGateway.keys.VerificationKey(ctx, issuer.IssuerURL, kid)
			return key, lookupErr
		},
		jwt.WithValidMethods(issuer.algorithms()),
		jwt.WithIssuer(issuer.IssuerURL),
		jwt.WithAudience(issuer.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(upstreamGatewayLeeway),
		jwt.WithTimeFunc(s.now),
	)
	if lookupErr != nil && !errors.Is(lookupErr, ErrInvalidToken) {
		return nil, lookupErr
	}
	if err != nil || claims.IssuedAt == nil ||
		claims.ExpiresAt.Sub(claims.IssuedAt.Time) > issuer.maxLifetime() || !slices.Contains(issuer.Subjects, claims.Subject) {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func (s *Service) UpstreamGatewayForSessionToken(issuerKey *APIKey, session *DelegatedSession) *UpstreamGatewayPrincipal {
	if issuerKey == nil || session == nil {
		return nil
	}
	for _, issuer := range s.upstreamGateway.issuers {
		if issuerKey.KeyHash == SessionIssuerKeyHash(issuer.SessionIssuerKID) {
			return &UpstreamGatewayPrincipal{IssuerURL: issuer.IssuerURL, Subject: session.SessionID, SessionID: session.SessionID}
		}
	}
	return nil
}
