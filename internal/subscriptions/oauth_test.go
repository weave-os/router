package subscriptions_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"workweave/router/internal/subscriptions"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestOAuthClientRefreshesCodexAndExtractsStableAccountID(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}`))
	client := subscriptions.NewOAuthClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "application/x-www-form-urlencoded", request.Header.Get("Content-Type"))
		require.NoError(t, request.ParseForm())
		require.Equal(t, subscriptions.CodexClientID, request.Form.Get("client_id"))
		require.Equal(t, "refresh-secret", request.Form.Get("refresh_token"))
		body := `{"access_token":"header.` + payload + `.sig","refresh_token":"rotated-secret","expires_in":900}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}, "https://token.test/codex", "", func() time.Time { return now })

	token, err := client.Refresh(context.Background(), subscriptions.ProviderCodex, "refresh-secret")
	require.NoError(t, err)
	require.Equal(t, "acct-1", token.AccountID)
	require.Equal(t, "rotated-secret", token.RefreshToken)
	require.Equal(t, now.Add(15*time.Minute), token.ExpiresAt)
}

func TestOAuthClientClassifiesRejectedRefreshWithoutLeakingToken(t *testing.T) {
	client := subscriptions.NewOAuthClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"refresh_token":"should-not-leak"}`)), Header: make(http.Header)}, nil
	})}, "", "https://token.test/claude", nil)

	_, err := client.Refresh(context.Background(), subscriptions.ProviderClaude, "refresh-secret")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "refresh-secret")
	require.NotContains(t, err.Error(), "should-not-leak")
	var refreshErr *subscriptions.OAuthRefreshError
	require.ErrorAs(t, err, &refreshErr)
	require.True(t, refreshErr.Terminal())
}

func TestOAuthClientRefreshesClaudeWithJSONRequest(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	client := subscriptions.NewOAuthClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "application/json", request.Header.Get("Content-Type"))
		requestBody, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"client_id":"`+subscriptions.ClaudeClientID+`","grant_type":"refresh_token","refresh_token":"refresh-secret"}`, string(requestBody))
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"access-new","refresh_token":"refresh-new","expires_in":1800}`)),
			Header:     make(http.Header),
		}, nil
	})}, "", "https://token.test/claude", func() time.Time { return now })

	token, err := client.Refresh(context.Background(), subscriptions.ProviderClaude, "refresh-secret")
	require.NoError(t, err)
	require.Equal(t, "access-new", token.AccessToken)
	require.Equal(t, "refresh-new", token.RefreshToken)
	require.Equal(t, now.Add(30*time.Minute), token.ExpiresAt)
}
