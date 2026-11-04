package subscriptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func unsignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestCodexBrowserLoginPollsUntilApprovedThenReturnsIdentity(t *testing.T) {
	approved := false
	var exchanged url.Values
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			_, _ = w.Write([]byte(`{"device_auth_id":"dev-1","user_code":"ABCD-1234","interval":"7"}`))
		case "/api/accounts/deviceauth/token":
			if !approved {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"authorization_code":"auth-code","code_verifier":"verifier"}`))
		case "/oauth/token":
			require.NoError(t, r.ParseForm())
			exchanged = r.PostForm
			idToken := unsignedJWT(t, map[string]any{"email": "dev@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace-1"}})
			_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","id_token":"` + idToken + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	client := NewOAuthClient(issuer.Client(), issuer.URL+"/oauth/token", "", func() time.Time { return now })

	started, err := client.StartLogin(context.Background(), ProviderCodex)
	require.NoError(t, err)
	require.Equal(t, issuer.URL+"/codex/device", started.VerificationURL)
	require.Equal(t, "ABCD-1234", started.UserCode)
	require.Equal(t, 7*time.Second, started.PollInterval)

	_, err = client.CompleteLogin(context.Background(), started.Pending, "")
	require.ErrorIs(t, err, ErrLoginPending)

	approved = true
	completed, err := client.CompleteLogin(context.Background(), started.Pending, "")
	require.NoError(t, err)
	require.Equal(t, CompletedLogin{Provider: ProviderCodex, ExternalAccountID: "workspace-1", DisplayName: "dev@example.com", RefreshToken: "rt"}, completed)
	require.Equal(t, "verifier", exchanged.Get("code_verifier"))
	require.Equal(t, issuer.URL+"/deviceauth/callback", exchanged.Get("redirect_uri"))

	now = now.Add(LoginTTL)
	_, err = client.CompleteLogin(context.Background(), started.Pending, "")
	require.ErrorContains(t, err, "expired")
}

func TestClaudeBrowserLoginExchangesPastedCodeForItsOwnState(t *testing.T) {
	var exchanged map[string]string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&exchanged))
		_, _ = w.Write([]byte(`{"refresh_token":"rt","account":{"uuid":"11111111-1111-4111-8111-111111111111","email_address":"dev@example.com"},"organization":{"uuid":"22222222-2222-4222-8222-222222222222","name":"Acme"}}`))
	}))
	defer tokenServer.Close()
	client := NewOAuthClient(tokenServer.Client(), "", tokenServer.URL, time.Now)

	started, err := client.StartLogin(context.Background(), ProviderClaude)
	require.NoError(t, err)
	authorize, err := url.Parse(started.VerificationURL)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(started.VerificationURL, DefaultClaudeAuthorizeURL+"?"))
	state := authorize.Query().Get("state")
	require.Equal(t, started.Pending.State, state)
	require.Equal(t, "S256", authorize.Query().Get("code_challenge_method"))

	_, err = client.CompleteLogin(context.Background(), started.Pending, "code#other-state")
	require.ErrorContains(t, err, "state did not match")
	require.Nil(t, exchanged, "a mismatched state never reaches the token endpoint")

	completed, err := client.CompleteLogin(context.Background(), started.Pending, " code#"+state+" ")
	require.NoError(t, err)
	require.Equal(t, CompletedLogin{
		Provider:          ProviderClaude,
		ExternalAccountID: "11111111-1111-4111-8111-111111111111:22222222-2222-4222-8222-222222222222",
		DisplayName:       "Acme: dev@example.com",
		RefreshToken:      "rt",
	}, completed)
	require.Equal(t, started.Pending.Verifier, exchanged["code_verifier"])
	require.Equal(t, "code", exchanged["code"])
}
