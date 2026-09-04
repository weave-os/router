package subscriptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"weave-os/router/internal/observability"
)

const (
	CodexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	ClaudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	DefaultCodexTokenURL  = "https://auth.openai.com/oauth/token"
	DefaultClaudeTokenURL = "https://console.anthropic.com/v1/oauth/token"
	defaultTokenLifetime  = time.Hour
	maxTokenResponseBytes = 1 << 20

	// tokenUserAgent identifies the router on provider token endpoints. Both
	// issuers rate-limit generic client user agents, so refresh must not rely on
	// the transport default.
	tokenUserAgent = "weave-router/1.0"
)

// RefreshedToken is the provider response needed to serve a subscription turn.
type RefreshedToken struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
	ExpiresAt    time.Time
}

// TokenRefresher exchanges provider refresh tokens for short-lived access tokens.
type TokenRefresher interface {
	Refresh(context.Context, Provider, string) (RefreshedToken, error)
}

// OAuthClient refreshes Claude and Codex subscription credentials.
type OAuthClient struct {
	httpClient     *http.Client
	codexTokenURL  string
	claudeTokenURL string
	now            func() time.Time
}

// NewOAuthClient constructs the provider token client. Empty URLs select the
// public provider endpoints; explicit URLs support self-hosted tests.
func NewOAuthClient(httpClient *http.Client, codexTokenURL, claudeTokenURL string, now func() time.Time) *OAuthClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	httpClient = observability.WrapHTTPClient(httpClient)
	if codexTokenURL == "" {
		codexTokenURL = DefaultCodexTokenURL
	}
	if claudeTokenURL == "" {
		claudeTokenURL = DefaultClaudeTokenURL
	}
	if now == nil {
		now = time.Now
	}
	return &OAuthClient{httpClient: httpClient, codexTokenURL: codexTokenURL, claudeTokenURL: claudeTokenURL, now: now}
}

// OAuthRefreshError classifies provider token failures without retaining a
// response body that could contain credential material.
type OAuthRefreshError struct {
	Provider Provider
	Status   int
}

func (e *OAuthRefreshError) Error() string {
	return fmt.Sprintf("%s subscription token refresh failed with status %d", e.Provider, e.Status)
}

// Terminal reports whether retrying the same refresh token is unsafe or futile.
func (e *OAuthRefreshError) Terminal() bool {
	return e.Status == http.StatusBadRequest || e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

func (c *OAuthClient) Refresh(ctx context.Context, provider Provider, refreshToken string) (RefreshedToken, error) {
	switch provider {
	case ProviderClaude:
		return c.refreshClaude(ctx, refreshToken)
	case ProviderCodex:
		return c.refreshCodex(ctx, refreshToken)
	default:
		return RefreshedToken{}, ErrProviderMismatch
	}
}

func (c *OAuthClient) refreshCodex(ctx context.Context, refreshToken string) (RefreshedToken, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {CodexClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return RefreshedToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", tokenUserAgent)
	var response struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := c.doTokenRequest(req, ProviderCodex, &response); err != nil {
		return RefreshedToken{}, err
	}
	if response.RefreshToken == "" {
		response.RefreshToken = refreshToken
	}
	accountID := accountIDFromJWT(response.IDToken)
	if accountID == "" {
		accountID = accountIDFromJWT(response.AccessToken)
	}
	return c.validatedToken(ProviderCodex, response.AccessToken, response.RefreshToken, accountID, response.ExpiresIn)
}

func (c *OAuthClient) refreshClaude(ctx context.Context, refreshToken string) (RefreshedToken, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     ClaudeClientID,
	})
	if err != nil {
		return RefreshedToken{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.claudeTokenURL, strings.NewReader(string(body)))
	if err != nil {
		return RefreshedToken{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", tokenUserAgent)
	var response struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := c.doTokenRequest(req, ProviderClaude, &response); err != nil {
		return RefreshedToken{}, err
	}
	if response.RefreshToken == "" {
		response.RefreshToken = refreshToken
	}
	return c.validatedToken(ProviderClaude, response.AccessToken, response.RefreshToken, "", response.ExpiresIn)
}

func (c *OAuthClient) doTokenRequest(req *http.Request, provider Provider, response any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxTokenResponseBytes))
		return &OAuthRefreshError{Provider: provider, Status: resp.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxTokenResponseBytes)).Decode(response)
}

func (c *OAuthClient) validatedToken(provider Provider, accessToken, refreshToken, accountID string, expiresIn int64) (RefreshedToken, error) {
	if accessToken == "" || refreshToken == "" {
		return RefreshedToken{}, fmt.Errorf("%s subscription token response omitted required credentials", provider)
	}
	lifetime := defaultTokenLifetime
	if expiresIn > 0 {
		lifetime = time.Duration(expiresIn) * time.Second
	}
	return RefreshedToken{AccessToken: accessToken, RefreshToken: refreshToken, AccountID: accountID, ExpiresAt: c.now().Add(lifetime)}, nil
}

func accountIDFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
		Organizations    []struct {
			ID string `json:"id"`
		} `json:"organizations"`
		OpenAIAuth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	if claims.ChatGPTAccountID != "" {
		return claims.ChatGPTAccountID
	}
	if claims.OpenAIAuth.ChatGPTAccountID != "" {
		return claims.OpenAIAuth.ChatGPTAccountID
	}
	if len(claims.Organizations) > 0 {
		return claims.Organizations[0].ID
	}
	return ""
}
