// Package anthropicfederation exchanges workload assertions for Anthropic API credentials.
package anthropicfederation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

const tokenEndpoint = auth.AnthropicFederationAudience + "/v1/oauth/token"
const jwtBearerGrant = "urn:ietf:params:oauth:grant-type:jwt-bearer"
const exchangeTimeout = 15 * time.Second
const maxResponseBytes = 64 * 1024

var errTemporaryExchange = errors.New("Anthropic token endpoint temporarily unavailable")

type assertionKey struct {
	issuer  string
	tokenID string
}

type cachedToken struct {
	token       []byte
	refreshAt   time.Time
	mandatoryAt time.Time
	retryAt     time.Time
}

// Source isolates cached tokens by key, installation and federation configuration.
// The identity provider is called for every exchange, including refreshes.
type Source struct {
	bindings      map[string]auth.AnthropicFederationConfig
	httpClient    *http.Client
	identityToken func(context.Context, string) (string, error)
	now           auth.Clock
	mu            sync.Mutex
	cache         *lru.Cache[string, cachedToken]
	assertions    *lru.Cache[assertionKey, struct{}]
	group         singleflight.Group
}

// NewSource refuses redirects even when the injected HTTP client follows them.
// Assertions and response bodies are never included in errors.
func NewSource(client *http.Client, identityToken func(context.Context, string) (string, error), now auth.Clock) *Source {
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cache, _ := lru.New[string, cachedToken](1024)
	assertions, _ := lru.New[assertionKey, struct{}](4096)
	return &Source{httpClient: observability.WrapHTTPClient(&isolatedClient), identityToken: identityToken, now: now, cache: cache, assertions: assertions}
}

// Token permits a cached credential during temporary advisory-refresh failures;
// failures within the mandatory window fail closed.
func (s *Source) Token(ctx context.Context, key *auth.ExternalAPIKey) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	federation, err := auth.NormalizeAnthropicFederation(key.AuthType, key.Provider, key.BaseURL, key.FederationConfig)
	if err != nil {
		return nil, err
	}
	if federation == nil {
		return nil, auth.ErrInvalidAnthropicFederation
	}
	if s.bindings != nil {
		approved, exists := s.bindings[key.InstallationID]
		if !exists || approved != *federation {
			return nil, fmt.Errorf("%w: federation target is not approved for this installation", auth.ErrAnthropicFederationUnavailable)
		}
	}
	encoded, _ := json.Marshal(federation)
	cacheKey := key.InstallationID + "\x00" + key.ID + "\x00" + string(encoded)
	if token := s.cached(cacheKey); token != nil {
		return token, nil
	}
	flight := s.group.DoChan(cacheKey, func() (any, error) {
		if token := s.cached(cacheKey); token != nil {
			return token, nil
		}
		exchangeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), exchangeTimeout)
		defer cancel()
		token, exchangeErr := s.exchange(exchangeCtx, federation, cacheKey)
		if exchangeErr == nil {
			return token, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		cached, exists := s.cache.Get(cacheKey)
		if exists && s.now().Before(cached.mandatoryAt) && errors.Is(exchangeErr, errTemporaryExchange) {
			cached.retryAt = s.now().Add(5 * time.Second)
			if cached.mandatoryAt.Before(cached.retryAt) {
				cached.retryAt = cached.mandatoryAt
			}
			s.cache.Add(cacheKey, cached)
			return append([]byte(nil), cached.token...), nil
		}
		return nil, exchangeErr
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case flightResult := <-flight:
		if flightResult.Err != nil {
			return nil, flightResult.Err
		}
		return append([]byte(nil), flightResult.Val.([]byte)...), nil
	}
}

func (s *Source) cached(cacheKey string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	cached, exists := s.cache.Get(cacheKey)
	if exists && (s.now().Before(cached.refreshAt) || (s.now().Before(cached.retryAt) && s.now().Before(cached.mandatoryAt))) {
		return append([]byte(nil), cached.token...)
	}
	return nil
}

func (s *Source) exchange(ctx context.Context, federation *auth.AnthropicFederationConfig, cacheKey string) ([]byte, error) {
	assertion, err := s.identityToken(ctx, federation.Audience)
	if err != nil {
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, fs.ErrNotExist) || errors.As(err, &networkError) {
			return nil, fmt.Errorf("%w: %w: acquire identity assertion", auth.ErrAnthropicFederationUnavailable, errTemporaryExchange)
		}
		return nil, fmt.Errorf("%w: acquire identity assertion", auth.ErrAnthropicFederationUnavailable)
	}
	if err := s.claimAssertion(assertion); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(struct {
		GrantType        string `json:"grant_type"`
		Assertion        string `json:"assertion"`
		FederationRuleID string `json:"federation_rule_id"`
		OrganizationID   string `json:"organization_id"`
		ServiceAccountID string `json:"service_account_id"`
		WorkspaceID      string `json:"workspace_id,omitempty"`
	}{jwtBearerGrant, assertion, federation.FederationRuleID, federation.OrganizationID, federation.ServiceAccountID, federation.WorkspaceID})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: build exchange request", auth.ErrAnthropicFederationUnavailable)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("anthropic-beta", string(auth.AnthropicBetaOAuth)+","+string(auth.AnthropicBetaFederation))
	started := s.now()
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", auth.ErrAnthropicFederationUnavailable, errTemporaryExchange)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
			return nil, fmt.Errorf("%w: %w (HTTP %d)", auth.ErrAnthropicFederationUnavailable, errTemporaryExchange, response.StatusCode)
		}
		return nil, fmt.Errorf("%w: exchange rejected (HTTP %d); check Anthropic authentication history", auth.ErrAnthropicFederationUnavailable, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", auth.ErrAnthropicFederationUnavailable, errTemporaryExchange)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("%w: exchange response exceeds limit", auth.ErrAnthropicFederationUnavailable)
	}
	var credential struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(body, &credential) != nil || credential.AccessToken == "" || strings.ContainsAny(credential.AccessToken, "\r\n \t") || !strings.EqualFold(credential.TokenType, "Bearer") || credential.ExpiresIn < 60 || credential.ExpiresIn > 86400 {
		return nil, fmt.Errorf("%w: invalid access token response", auth.ErrAnthropicFederationUnavailable)
	}
	lifetime := time.Duration(credential.ExpiresIn) * time.Second
	mandatoryAt := started.Add(lifetime - min(30*time.Second, lifetime/10))
	if !s.now().Before(mandatoryAt) {
		return nil, fmt.Errorf("%w: access token is too close to expiry", auth.ErrAnthropicFederationUnavailable)
	}
	token := []byte(credential.AccessToken)
	s.mu.Lock()
	s.cache.Add(cacheKey, cachedToken{token: token, refreshAt: started.Add(lifetime - min(120*time.Second, lifetime/5)), mandatoryAt: mandatoryAt})
	s.mu.Unlock()
	return append([]byte(nil), token...), nil
}

// claimAssertion prevents replay of jti-bearing assertions, including after an
// ambiguous transport failure. The IdP remains responsible for rotation across restarts.
func (s *Source) claimAssertion(assertion string) error {
	if len(assertion) > 16*1024 {
		return fmt.Errorf("%w: expected a raw OIDC JWT", auth.ErrAnthropicFederationUnavailable)
	}
	// This parse is only a local replay guard. Anthropic verifies the signature
	// and enforces the issuer and rule; these claims never authorize a request.
	claims := jwt.RegisteredClaims{}
	if _, _, err := new(jwt.Parser).ParseUnverified(assertion, &claims); err != nil {
		return fmt.Errorf("%w: invalid identity assertion", auth.ErrAnthropicFederationUnavailable)
	}
	if claims.ID == "" {
		return nil
	}
	if len(claims.Issuer) > 2048 || len(claims.ID) > 2048 {
		return fmt.Errorf("%w: assertion replay identifiers exceed the limit", auth.ErrAnthropicFederationUnavailable)
	}
	assertionID := assertionKey{issuer: claims.Issuer, tokenID: claims.ID}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assertions.Contains(assertionID) {
		return fmt.Errorf("%w: %w: identity assertion was already used; rotate the projected token or mint a fresh assertion", auth.ErrAnthropicFederationUnavailable, errTemporaryExchange)
	}
	s.assertions.Add(assertionID, struct{}{})
	return nil
}

var _ auth.AnthropicFederationTokenSource = (*Source)(nil)
