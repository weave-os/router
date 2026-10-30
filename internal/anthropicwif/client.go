// Package anthropicwif exchanges deployment OIDC assertions for Claude API tokens.
package anthropicwif

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

const (
	tokenURL         = "https://api.anthropic.com/v1/oauth/token"
	grantType        = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	exchangeTimeout  = 10 * time.Second
	maxResponseBytes = 64 * 1024
	advisoryRefresh  = 120 * time.Second
	mandatoryRefresh = 30 * time.Second
)

// IdentityTokenSource supplies a fresh, unwrapped OIDC JWT on every exchange.
// Sources must not reuse single-use assertions across calls or process replicas.
type IdentityTokenSource interface {
	IdentityToken(context.Context) ([]byte, error)
}

type cachedToken struct {
	token     []byte
	expiresAt time.Time
}

// Client caches access tokens separately for each stored federation configuration.
type Client struct {
	httpClient *http.Client
	identity   IdentityTokenSource
	now        auth.Clock
	cache      *lru.Cache[string, cachedToken]
	group      singleflight.Group
}

func NewClient(client *http.Client, identity IdentityTokenSource, now auth.Clock) *Client {
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = exchangeTimeout
	// Assertions must never follow redirects to another endpoint.
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if now == nil {
		now = time.Now
	}
	cache, err := lru.New[string, cachedToken](1024)
	if err != nil {
		panic(err)
	}
	return &Client{httpClient: observability.WrapHTTPClient(&bounded), identity: identity, now: now, cache: cache}
}

func (c *Client) Token(ctx context.Context, key *auth.ExternalAPIKey) ([]byte, error) {
	if key == nil || c.identity == nil {
		return nil, auth.ErrAnthropicWIFUnavailable
	}
	cfg, err := auth.NormalizeAnthropicWIF(key.Provider, auth.AuthTypeAnthropicWIF, auth.AnthropicWIFConfig{
		FederationRuleID: key.FederationRuleID, OrganizationID: key.OrganizationID,
		ServiceAccountID: key.ServiceAccountID, WorkspaceID: key.WorkspaceID,
	})
	if err != nil {
		return nil, err
	}
	encodedKey, _ := json.Marshal([]string{key.ID, cfg.FederationRuleID, cfg.OrganizationID, cfg.ServiceAccountID, cfg.WorkspaceID})
	cacheKey := string(encodedKey)
	if cached, ok := c.cache.Get(cacheKey); ok && c.now().Before(cached.expiresAt.Add(-advisoryRefresh)) {
		return bytes.Clone(cached.token), nil
	}
	// One caller cancelling must not abort the shared exchange for other waiters.
	response := c.group.DoChan(cacheKey, func() (any, error) {
		exchangeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), exchangeTimeout)
		defer cancel()
		cached, ok := c.cache.Get(cacheKey)
		if ok && c.now().Before(cached.expiresAt.Add(-advisoryRefresh)) {
			return cached.token, nil
		}
		token, err := c.exchange(exchangeCtx, cfg)
		if err != nil {
			if ok && c.now().Before(cached.expiresAt.Add(-mandatoryRefresh)) {
				return cached.token, nil
			}
			return nil, err
		}
		c.cache.Add(cacheKey, token)
		return token.token, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-response:
		if result.Err != nil {
			return nil, result.Err
		}
		return bytes.Clone(result.Val.([]byte)), nil
	}
}

func (c *Client) exchange(ctx context.Context, cfg auth.AnthropicWIFConfig) (cachedToken, error) {
	assertion, err := c.identity.IdentityToken(ctx)
	if err != nil || len(bytes.TrimSpace(assertion)) == 0 {
		// An identity provider error can include the assertion or a token-bearing URL.
		return cachedToken{}, fmt.Errorf("%w: obtain identity assertion", auth.ErrAnthropicWIFUnavailable)
	}
	payload := struct {
		GrantType        string `json:"grant_type"`
		Assertion        string `json:"assertion"`
		FederationRuleID string `json:"federation_rule_id"`
		OrganizationID   string `json:"organization_id"`
		ServiceAccountID string `json:"service_account_id"`
		WorkspaceID      string `json:"workspace_id,omitempty"`
	}{grantType, string(bytes.TrimSpace(assertion)), cfg.FederationRuleID, cfg.OrganizationID, cfg.ServiceAccountID, cfg.WorkspaceID}
	body, err := json.Marshal(payload)
	if err != nil {
		return cachedToken{}, fmt.Errorf("%w: encode exchange", auth.ErrAnthropicWIFUnavailable)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return cachedToken{}, fmt.Errorf("%w: build exchange", auth.ErrAnthropicWIFUnavailable)
	}
	request.Header.Set("Content-Type", "application/json")
	startedAt := c.now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		return cachedToken{}, fmt.Errorf("%w: token exchange failed", auth.ErrAnthropicWIFUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return cachedToken{}, fmt.Errorf("%w: token endpoint returned status %d", auth.ErrAnthropicWIFUnavailable, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return cachedToken{}, fmt.Errorf("%w: read token response", auth.ErrAnthropicWIFUnavailable)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	// Bound duration conversion and refuse credentials already inside the safety margin.
	if json.Unmarshal(raw, &token) != nil || strings.TrimSpace(token.AccessToken) == "" || strings.ContainsAny(token.AccessToken, "\r\n") || token.ExpiresIn <= int64(mandatoryRefresh/time.Second) || token.ExpiresIn > int64((1<<63-1)/int64(time.Second)) {
		return cachedToken{}, fmt.Errorf("%w: invalid token response", auth.ErrAnthropicWIFUnavailable)
	}
	expiresAt := startedAt.Add(time.Duration(token.ExpiresIn) * time.Second)
	if !c.now().Before(expiresAt.Add(-mandatoryRefresh)) {
		return cachedToken{}, fmt.Errorf("%w: token expired during exchange", auth.ErrAnthropicWIFUnavailable)
	}
	return cachedToken{token: []byte(token.AccessToken), expiresAt: expiresAt}, nil
}

var _ auth.AnthropicWIFTokenSource = (*Client)(nil)
