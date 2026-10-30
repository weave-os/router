package anthropicwif_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/anthropicwif"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type identityFunc func(context.Context) ([]byte, error)

func (f identityFunc) IdentityToken(ctx context.Context) ([]byte, error) { return f(ctx) }

func federationKey() *auth.ExternalAPIKey {
	return &auth.ExternalAPIKey{ID: "test-key", Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, FederationRuleID: "fdrl_test", OrganizationID: "test-org", ServiceAccountID: "svac_test", WorkspaceID: "wrkspc_test"}
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestExchangeCachesRefreshesAndReadsFreshAssertions(t *testing.T) {
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var assertions, exchanges int
	identity := identityFunc(func(context.Context) ([]byte, error) {
		assertions++
		return []byte(fmt.Sprintf("fresh-jwt-%d", assertions)), nil
	})
	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		exchanges++
		assert.Equal(t, "https://api.anthropic.com/v1/oauth/token", req.URL.String())
		assert.Equal(t, http.MethodPost, req.Method)
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		var payload map[string]string
		require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
		assert.Equal(t, map[string]string{
			"grant_type": "urn:ietf:params:oauth:grant-type:jwt-bearer", "assertion": fmt.Sprintf("fresh-jwt-%d", exchanges),
			"federation_rule_id": "fdrl_test", "organization_id": "test-org", "service_account_id": "svac_test", "workspace_id": "wrkspc_test",
		}, payload)
		return response(http.StatusOK, fmt.Sprintf(`{"access_token":"token-%d","expires_in":3600}`, exchanges)), nil
	})}
	source := anthropicwif.NewClient(client, identity, func() time.Time { return current })
	key := federationKey()
	token, err := source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "token-1", string(token))
	token[0] = 'X'
	token, err = source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "token-1", string(token), "callers cannot mutate the cached credential")
	current = current.Add(3479 * time.Second)
	_, err = source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, 1, exchanges)
	current = current.Add(time.Second)
	token, err = source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "token-2", string(token))
	assert.Equal(t, 2, assertions)
}

func TestRefreshOutageUsesSafeTokenThenFailsClosed(t *testing.T) {
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	source := anthropicwif.NewClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(http.StatusOK, `{"access_token":"cached-token","expires_in":3600}`), nil
		}
		return response(http.StatusUnauthorized, `{"error":"secret-assertion-must-not-leak"}`), nil
	})}, identityFunc(func(context.Context) ([]byte, error) { return []byte("fresh-assertion"), nil }), func() time.Time { return current })
	_, err := source.Token(context.Background(), federationKey())
	require.NoError(t, err)
	current = current.Add(3480 * time.Second)
	token, err := source.Token(context.Background(), federationKey())
	require.NoError(t, err)
	assert.Equal(t, "cached-token", string(token))
	_, err = source.Token(context.Background(), federationKey())
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "refresh failures use a short retry backoff while the cached token remains safe")
	current = current.Add(5 * time.Second)
	current = current.Add(90 * time.Second)
	token, err = source.Token(context.Background(), federationKey())
	require.ErrorIs(t, err, auth.ErrAnthropicWIFUnavailable)
	assert.Empty(t, token)
	assert.NotContains(t, err.Error(), "secret-assertion")
	assert.Equal(t, 3, calls)
}

func TestFederationMetadataInvalidatesCache(t *testing.T) {
	var calls int
	source := anthropicwif.NewClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return response(http.StatusOK, `{"access_token":"access-token","expires_in":3600}`), nil
	})}, identityFunc(func(context.Context) ([]byte, error) { return []byte("fresh-assertion"), nil }), time.Now)
	key := federationKey()
	for _, update := range []func(){func() {}, func() { key.WorkspaceID = "other-workspace" }, func() { key.ServiceAccountID = "other-service" }, func() { key.OrganizationID = "other-org" }, func() { key.FederationRuleID = "other-rule" }, func() { key.ID = "replacement-key" }} {
		update()
		_, err := source.Token(context.Background(), key)
		require.NoError(t, err)
	}
	assert.Equal(t, 6, calls)
}

func TestExchangeRejectsInvalidResponsesAndRedirects(t *testing.T) {
	for _, body := range []string{`{`, `{}`, `{"access_token":"token","expires_in":0}`, `{"access_token":"token","expires_in":30}`, `{"access_token":"token","expires_in":9223372036854775807}`, strings.Repeat("x", 65537)} {
		t.Run(fmt.Sprintf("body-%d", len(body)), func(t *testing.T) {
			source := anthropicwif.NewClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(http.StatusOK, body), nil })}, identityFunc(func(context.Context) ([]byte, error) { return []byte("assertion"), nil }), time.Now)
			token, err := source.Token(context.Background(), federationKey())
			require.ErrorIs(t, err, auth.ErrAnthropicWIFUnavailable)
			assert.Empty(t, token)
			assert.NotContains(t, err.Error(), "assertion")
		})
	}
	calls := 0
	source := anthropicwif.NewClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		r := response(http.StatusTemporaryRedirect, "")
		r.Header.Set("Location", "https://untrusted.example/token")
		return r, nil
	})}, identityFunc(func(context.Context) ([]byte, error) { return []byte("assertion"), nil }), time.Now)
	_, err := source.Token(context.Background(), federationKey())
	require.ErrorIs(t, err, auth.ErrAnthropicWIFUnavailable)
	assert.Equal(t, 1, calls, "an assertion must never follow a redirect")
}

func TestIdentityFailureDoesNotLeakSecrets(t *testing.T) {
	source := anthropicwif.NewClient(nil, identityFunc(func(context.Context) ([]byte, error) { return nil, errors.New("secret assertion value") }), time.Now)
	_, err := source.Token(context.Background(), federationKey())
	require.ErrorIs(t, err, auth.ErrAnthropicWIFUnavailable)
	assert.NotContains(t, err.Error(), "secret assertion value")
}

func TestConcurrentRequestsShareExchange(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	source := anthropicwif.NewClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return response(http.StatusOK, `{"access_token":"shared-token","expires_in":3600}`), nil
	})}, identityFunc(func(context.Context) ([]byte, error) { return []byte("single-use-assertion"), nil }), time.Now)
	var wait sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		wait.Go(func() {
			token, err := source.Token(context.Background(), federationKey())
			if err == nil && string(token) != "shared-token" {
				err = fmt.Errorf("unexpected token")
			}
			results <- err
		})
	}
	<-entered
	close(release)
	wait.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), calls.Load())
}
