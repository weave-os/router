package anthropicfederation_test

import (
	"context"
	"encoding/base64"
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
	"weave-os/router/internal/anthropicfederation"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func federationKey() *auth.ExternalAPIKey {
	return &auth.ExternalAPIKey{ID: "synthetic-key", InstallationID: "synthetic-installation", Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF,
		FederationConfig: &auth.AnthropicFederationConfig{FederationRuleID: "fdrl_synthetic", OrganizationID: "00000000-0000-0000-0000-000000000001", ServiceAccountID: "svac_synthetic", WorkspaceID: "wrkspc_synthetic"}}
}

func assertion(jti string) string {
	claims, _ := json.Marshal(map[string]string{"iss": "https://issuer.example.com", "sub": "synthetic-workload", "jti": jti})
	return "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

const tokenResponse = `{"access_token":"sk-ant-oat01-synthetic","token_type":"Bearer","expires_in":600}`

func TestExchangeCachesAndRefreshesWithFreshAssertion(t *testing.T) {
	var calls, assertions int
	current := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "https://api.anthropic.com/v1/oauth/token", req.URL.String())
		assert.Equal(t, http.MethodPost, req.Method)
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assert.Empty(t, req.Header.Get("Authorization"))
		var exchange map[string]string
		require.NoError(t, json.NewDecoder(req.Body).Decode(&exchange))
		assert.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", exchange["grant_type"])
		assert.Equal(t, assertion(fmt.Sprint(assertions)), exchange["assertion"])
		assert.Equal(t, "fdrl_synthetic", exchange["federation_rule_id"])
		assert.Equal(t, "svac_synthetic", exchange["service_account_id"])
		assert.Equal(t, "00000000-0000-0000-0000-000000000001", exchange["organization_id"])
		assert.Equal(t, "wrkspc_synthetic", exchange["workspace_id"])
		return response(http.StatusOK, tokenResponse), nil
	})}
	source := anthropicfederation.NewSource(client, func(_ context.Context, audience string) (string, error) {
		assert.Equal(t, auth.AnthropicFederationAudience, audience)
		assertions++
		return assertion(fmt.Sprint(assertions)), nil
	}, func() time.Time { return current })
	key := federationKey()
	token, err := source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat01-synthetic", string(token))
	token[0] = 'X'
	cached, err := source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat01-synthetic", string(cached))
	assert.Equal(t, 1, calls)
	current = current.Add(480 * time.Second)
	_, err = source.Token(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, 2, assertions)
}

func TestTemporaryRefreshFailureUsesCachedTokenUntilMandatoryWindow(t *testing.T) {
	current := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	calls := 0
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			return response(http.StatusServiceUnavailable, "private provider diagnostic"), nil
		}
		return response(http.StatusOK, tokenResponse), nil
	})}
	source := anthropicfederation.NewSource(client, func(context.Context, string) (string, error) { return assertion(""), nil }, func() time.Time { return current })
	_, err := source.Token(context.Background(), federationKey())
	require.NoError(t, err)
	current = current.Add(480 * time.Second)
	token, err := source.Token(context.Background(), federationKey())
	require.NoError(t, err)
	assert.NotEmpty(t, token)
	_, err = source.Token(context.Background(), federationKey())
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "temporary failures are throttled during the advisory window")
	current = current.Add(90 * time.Second)
	_, err = source.Token(context.Background(), federationKey())
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
	assert.NotContains(t, err.Error(), "private provider diagnostic")
}

func TestCacheIsolatesInstallationAndFederationTarget(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(http.StatusOK, tokenResponse), nil
	})}
	source := anthropicfederation.NewSource(client, func(context.Context, string) (string, error) { return assertion(""), nil }, time.Now)
	key := federationKey()
	for _, mutate := range []func(){func() {}, func() { key.InstallationID = "other-installation" }, func() { key.FederationConfig.WorkspaceID = "wrkspc_other" }, func() { key.FederationConfig.FederationRuleID = "fdrl_other" }, func() { key.FederationConfig.ServiceAccountID = "svac_other" }, func() { key.FederationConfig.OrganizationID = "00000000-0000-0000-0000-000000000002" }, func() { key.FederationConfig.Audience = "another-audience" }} {
		mutate()
		_, err := source.Token(context.Background(), key)
		require.NoError(t, err)
	}
	assert.Equal(t, 7, calls)
}

func TestReplayedJTIIsNeverResent(t *testing.T) {
	current := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	calls := 0
	jwt := assertion("unique-assertion")
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(http.StatusOK, tokenResponse), nil
	})}
	source := anthropicfederation.NewSource(client, func(context.Context, string) (string, error) { return jwt, nil }, func() time.Time { return current })
	_, err := source.Token(context.Background(), federationKey())
	require.NoError(t, err)
	current = current.Add(480 * time.Second)
	_, err = source.Token(context.Background(), federationKey())
	require.NoError(t, err, "a replay cannot prevent using an already valid cached token during advisory refresh")
	current = current.Add(90 * time.Second)
	_, err = source.Token(context.Background(), federationKey())
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
	assert.Equal(t, 1, calls)
	jwt = assertion("rotated-assertion")
	_, err = source.Token(context.Background(), federationKey())
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

func TestExchangeRejectsInvalidResponsesWithoutLeakingSecrets(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `{"access_token":"private-token","token_type":"MAC","expires_in":600}`, `{"access_token":"private-token","token_type":"Bearer","expires_in":0}`, `{"access_token":"private-token","token_type":"Bearer","expires_in":9223372036854775807}`, strings.Repeat("private-token", 10000)} {
		t.Run(fmt.Sprint(len(body), body[:min(10, len(body))]), func(t *testing.T) {
			client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(http.StatusOK, body), nil })}
			source := anthropicfederation.NewSource(client, func(context.Context, string) (string, error) { return assertion(""), nil }, time.Now)
			_, err := source.Token(context.Background(), federationKey())
			require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
			assert.NotContains(t, err.Error(), "private-token")
		})
	}
}

func TestExchangeDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		redirect := response(http.StatusTemporaryRedirect, "")
		redirect.Header.Set("Location", "https://untrusted.example.com")
		return redirect, nil
	})}
	source := anthropicfederation.NewSource(client, func(context.Context, string) (string, error) { return assertion(""), nil }, time.Now)
	_, err := source.Token(context.Background(), federationKey())
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
	assert.EqualValues(t, 1, calls.Load())
}

func TestCancelledCallerDoesNotCancelSharedExchange(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return response(http.StatusOK, tokenResponse), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})}
	source := anthropicfederation.NewSource(client, func(context.Context, string) (string, error) { return assertion(""), nil }, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() { _, err := source.Token(ctx, federationKey()); leader <- err }()
	<-entered
	cancel()
	require.ErrorIs(t, <-leader, context.Canceled)
	close(release)
	_, err := source.Token(context.Background(), federationKey())
	require.NoError(t, err)
}

func TestIdentityFailureDoesNotSendExchange(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unexpected exchange")
		return nil, errors.New("unexpected exchange")
	})}
	source := anthropicfederation.NewSource(client, func(context.Context, string) (string, error) { return "", errors.New("identity unavailable") }, time.Now)
	_, err := source.Token(context.Background(), federationKey())
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
}
