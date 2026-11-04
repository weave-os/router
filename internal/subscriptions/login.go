package subscriptions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// DefaultClaudeAuthorizeURL starts Claude's paste-the-code OAuth flow, the
	// same one `router login claude` opens.
	DefaultClaudeAuthorizeURL = "https://claude.ai/oauth/authorize"
	claudeLoginRedirectURI    = "https://console.anthropic.com/oauth/code/callback"
	claudeLoginScope          = "org:create_api_key user:profile user:inference"

	// LoginTTL bounds how long a started browser login can be completed.
	LoginTTL = 15 * time.Minute
)

// ErrLoginPending reports that the user has not approved the device code yet.
var ErrLoginPending = errors.New("subscription login is waiting for approval")

// PendingLogin is the provider state a started browser login needs to finish.
// It carries PKCE and device secrets, so callers seal it before handing it out.
type PendingLogin struct {
	Provider     Provider  `json:"provider"`
	DeviceAuthID string    `json:"device_auth_id,omitempty"`
	UserCode     string    `json:"user_code,omitempty"`
	Verifier     string    `json:"verifier,omitempty"`
	State        string    `json:"state,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// StartedLogin is what the user sees: where to sign in and, for Codex, the
// code to confirm there.
type StartedLogin struct {
	Pending         PendingLogin
	VerificationURL string
	UserCode        string
	PollInterval    time.Duration
}

// CompletedLogin is an enrollable subscription credential.
type CompletedLogin struct {
	Provider          Provider
	ExternalAccountID string
	DisplayName       string
	RefreshToken      string
}

func (c *OAuthClient) codexIssuer() string {
	return strings.TrimSuffix(c.codexTokenURL, "/oauth/token")
}

func (c *OAuthClient) StartLogin(ctx context.Context, provider Provider) (StartedLogin, error) {
	expiresAt := c.now().Add(LoginTTL)
	switch provider {
	case ProviderCodex:
		var response struct {
			DeviceAuthID string      `json:"device_auth_id"`
			UserCode     string      `json:"user_code"`
			Interval     json.Number `json:"interval"`
		}
		if err := c.postJSON(ctx, provider, c.codexIssuer()+"/api/accounts/deviceauth/usercode", map[string]string{"client_id": CodexClientID}, &response); err != nil {
			return StartedLogin{}, err
		}
		if response.DeviceAuthID == "" || response.UserCode == "" {
			return StartedLogin{}, errors.New("Codex device authorization returned an invalid response")
		}
		interval := 5 * time.Second
		if seconds, err := response.Interval.Int64(); err == nil && seconds >= 1 && seconds <= 30 {
			interval = time.Duration(seconds) * time.Second
		}
		return StartedLogin{
			Pending:         PendingLogin{Provider: provider, DeviceAuthID: response.DeviceAuthID, UserCode: response.UserCode, ExpiresAt: expiresAt},
			VerificationURL: c.codexIssuer() + "/codex/device",
			UserCode:        response.UserCode,
			PollInterval:    interval,
		}, nil
	case ProviderClaude:
		verifier, err := randomURLToken()
		if err != nil {
			return StartedLogin{}, err
		}
		state, err := randomURLToken()
		if err != nil {
			return StartedLogin{}, err
		}
		challenge := sha256.Sum256([]byte(verifier))
		query := url.Values{
			"code":                  {"true"},
			"client_id":             {ClaudeClientID},
			"response_type":         {"code"},
			"redirect_uri":          {claudeLoginRedirectURI},
			"scope":                 {claudeLoginScope},
			"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
			"code_challenge_method": {"S256"},
			"state":                 {state},
		}
		return StartedLogin{
			Pending:         PendingLogin{Provider: provider, Verifier: verifier, State: state, ExpiresAt: expiresAt},
			VerificationURL: c.claudeAuthorizeURL + "?" + query.Encode(),
		}, nil
	default:
		return StartedLogin{}, fmt.Errorf("unsupported subscription provider %q", provider)
	}
}

// CompleteLogin finishes a started login. Codex returns ErrLoginPending until
// the user approves the device code; Claude needs the pasted `code#state`.
func (c *OAuthClient) CompleteLogin(ctx context.Context, pending PendingLogin, pastedCode string) (CompletedLogin, error) {
	if !c.now().Before(pending.ExpiresAt) {
		return CompletedLogin{}, errors.New("subscription login expired")
	}
	switch pending.Provider {
	case ProviderCodex:
		return c.completeCodexLogin(ctx, pending)
	case ProviderClaude:
		return c.completeClaudeLogin(ctx, pending, pastedCode)
	default:
		return CompletedLogin{}, fmt.Errorf("unsupported subscription provider %q", pending.Provider)
	}
}

func (c *OAuthClient) completeCodexLogin(ctx context.Context, pending PendingLogin) (CompletedLogin, error) {
	var authorization struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	err := c.postJSON(ctx, ProviderCodex, c.codexIssuer()+"/api/accounts/deviceauth/token", map[string]string{"device_auth_id": pending.DeviceAuthID, "user_code": pending.UserCode}, &authorization)
	var oauthErr *OAuthRefreshError
	if errors.As(err, &oauthErr) && oauthErr.Status < http.StatusInternalServerError {
		return CompletedLogin{}, ErrLoginPending
	}
	if err != nil {
		return CompletedLogin{}, err
	}
	if authorization.AuthorizationCode == "" || authorization.CodeVerifier == "" {
		return CompletedLogin{}, errors.New("Codex authorization returned an invalid response")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authorization.AuthorizationCode},
		"redirect_uri":  {c.codexIssuer() + "/deviceauth/callback"},
		"client_id":     {CodexClientID},
		"code_verifier": {authorization.CodeVerifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return CompletedLogin{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", tokenUserAgent)
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := c.doTokenRequest(req, ProviderCodex, &token); err != nil {
		return CompletedLogin{}, err
	}
	identity := token.IDToken
	if identity == "" {
		identity = token.AccessToken
	}
	accountID := accountIDFromJWT(identity)
	if token.RefreshToken == "" || accountID == "" {
		return CompletedLogin{}, errors.New("Codex token exchange omitted account identity or refresh credentials")
	}
	return CompletedLogin{Provider: ProviderCodex, ExternalAccountID: accountID, DisplayName: emailFromJWT(identity), RefreshToken: token.RefreshToken}, nil
}

func (c *OAuthClient) completeClaudeLogin(ctx context.Context, pending PendingLogin, pastedCode string) (CompletedLogin, error) {
	code, state, found := strings.Cut(strings.TrimSpace(pastedCode), "#")
	if !found || code == "" {
		return CompletedLogin{}, errors.New("expected Claude's code#state value")
	}
	if state != pending.State {
		return CompletedLogin{}, errors.New("Claude authorization state did not match this login")
	}
	var token struct {
		RefreshToken string `json:"refresh_token"`
		Account      struct {
			UUID  string `json:"uuid"`
			Email string `json:"email_address"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"organization"`
	}
	body := map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"state":         state,
		"client_id":     ClaudeClientID,
		"redirect_uri":  claudeLoginRedirectURI,
		"code_verifier": pending.Verifier,
	}
	if err := c.postJSON(ctx, ProviderClaude, c.claudeTokenURL, body, &token); err != nil {
		return CompletedLogin{}, err
	}
	externalID := ClaudeExternalAccountID(token.Account.UUID, token.Organization.UUID)
	if token.RefreshToken == "" || externalID == "" {
		return CompletedLogin{}, errors.New("Claude token exchange omitted account identity or refresh credentials")
	}
	displayName := token.Account.Email
	if token.Organization.Name != "" && displayName != "" {
		displayName = token.Organization.Name + ": " + displayName
	}
	return CompletedLogin{Provider: ProviderClaude, ExternalAccountID: externalID, DisplayName: displayName, RefreshToken: token.RefreshToken}, nil
}

func (c *OAuthClient) postJSON(ctx context.Context, provider Provider, endpoint string, body map[string]string, response any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", tokenUserAgent)
	return c.doTokenRequest(req, provider, response)
}

func randomURLToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func emailFromJWT(token string) string {
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		return ""
	}
	if email, ok := claims["email"].(string); ok && email != "" {
		return email
	}
	profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
	email, _ := profile["email"].(string)
	return email
}
