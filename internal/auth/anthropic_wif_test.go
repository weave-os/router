package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
)

func claudeFederationConfig() auth.AnthropicWIFConfig {
	return auth.AnthropicWIFConfig{FederationRuleID: "fdrl_test", OrganizationID: "test-org", ServiceAccountID: "svac_test"}
}

func TestAnthropicWIFValidation(t *testing.T) {
	for _, provider := range []string{providers.ProviderOpenAI, providers.ProviderAnthropicGateway} {
		_, err := auth.NormalizeAnthropicWIF(provider, auth.AuthTypeAnthropicWIF, claudeFederationConfig())
		require.ErrorIs(t, err, auth.ErrInvalidAnthropicWIFAuth)
	}
	for _, cfg := range []auth.AnthropicWIFConfig{{}, {FederationRuleID: "fdrl_test"}, {FederationRuleID: "fdrl_test", OrganizationID: "test-org"}} {
		_, err := auth.NormalizeAnthropicWIF(providers.ProviderAnthropic, auth.AuthTypeAnthropicWIF, cfg)
		require.ErrorIs(t, err, auth.ErrInvalidAnthropicWIFAuth)
	}
	cfg := claudeFederationConfig()
	cfg.WorkspaceID = "  wrkspc_test  "
	normalized, err := auth.NormalizeAnthropicWIF(providers.ProviderAnthropic, auth.AuthTypeAnthropicWIF, cfg)
	require.NoError(t, err)
	assert.Equal(t, "wrkspc_test", normalized.WorkspaceID)
}

func TestAnthropicWIFStoresOnlyMetadata(t *testing.T) {
	repo := &recordingExternalKeyRepo{}
	svc := makeServiceWithExternalKeys(t, repo)
	_, err := svc.UpsertExternalAPIKey(context.Background(), "install", auth.UpsertExternalAPIKeyParams{
		Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, AnthropicWIF: claudeFederationConfig(),
	})
	require.NoError(t, err)
	assert.Empty(t, repo.created.KeyCiphertext)
	assert.Empty(t, repo.created.KeyPrefix)
	assert.Empty(t, repo.created.KeySuffix)
	assert.Equal(t, "fdrl_test", *repo.created.FederationRuleID)
	assert.Equal(t, "test-org", *repo.created.OrganizationID)
	assert.Equal(t, "svac_test", *repo.created.ServiceAccountID)
	assert.Nil(t, repo.created.WorkspaceID)
	assert.Nil(t, repo.created.AuthAccount)
	assert.Nil(t, repo.created.AuthUser)
}

func TestAnthropicWIFRejectsSecretsAndEndpointOverrides(t *testing.T) {
	url := "https://custom.example.com"
	for _, params := range []auth.UpsertExternalAPIKeyParams{
		{Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, RawKey: "pasted-secret", AnthropicWIF: claudeFederationConfig()},
		{Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, BaseURL: &url, AnthropicWIF: claudeFederationConfig()},
		{Provider: providers.ProviderAnthropic, RawKey: "api-key", AnthropicWIF: claudeFederationConfig()},
	} {
		_, err := makeServiceWithExternalKeys(t, &recordingExternalKeyRepo{}).UpsertExternalAPIKey(context.Background(), "install", params)
		require.ErrorIs(t, err, auth.ErrInvalidAnthropicWIFAuth)
	}
}

type claudeTokenSource struct{ token []byte }

func (s claudeTokenSource) Token(context.Context, *auth.ExternalAPIKey) ([]byte, error) {
	return s.token, nil
}

func TestAnthropicWIFResolutionIsIsolatedFromSnowflake(t *testing.T) {
	for _, wired := range []bool{true, false} {
		t.Run(map[bool]string{true: "wired", false: "disabled"}[wired], func(t *testing.T) {
			key := &auth.ExternalAPIKey{ID: "claude-key", Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF}
			snowflake := wifKeyFor("install")
			repo := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{key, snowflake}}
			svc := makeServiceWithExternalKeys(t, repo).WithWIFTokenSource(&stubWIFSource{credential: []byte("WIF.GCP.snowflake-attestation")})
			if wired {
				svc.WithAnthropicWIFTokenSource(claudeTokenSource{token: []byte("sk-ant-oat01-federated-token")})
			}
			resolved, err := svc.ExternalAPIKeyWithCredential(context.Background(), "install", key.ID)
			if wired {
				require.NoError(t, err)
				assert.Equal(t, "sk-ant-oat01-federated-token", string(resolved.Plaintext))
			} else {
				require.ErrorIs(t, err, auth.ErrUpstreamCredentialUnavailable)
			}
			assert.Empty(t, key.Plaintext, "request tokens must not mutate the cached row")
			resolved, err = svc.ExternalAPIKeyWithCredential(context.Background(), "install", snowflake.ID)
			require.NoError(t, err)
			assert.Equal(t, "WIF.GCP.snowflake-attestation", string(resolved.Plaintext))
		})
	}
}
