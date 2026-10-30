package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
)

func federationConfiguration() *auth.AnthropicFederationConfig {
	return &auth.AnthropicFederationConfig{FederationRuleID: "fdrl_synthetic", OrganizationID: "00000000-0000-0000-0000-000000000001", ServiceAccountID: "svac_synthetic"}
}

type federationSourceFunc func(context.Context, *auth.ExternalAPIKey) ([]byte, error)

func (f federationSourceFunc) Token(ctx context.Context, key *auth.ExternalAPIKey) ([]byte, error) {
	return f(ctx, key)
}

func TestFederationUpsertPersistsNoSecret(t *testing.T) {
	repo := &recordingExternalKeyRepo{}
	svc := makeServiceWithExternalKeys(t, repo)
	_, err := svc.UpsertExternalAPIKey(context.Background(), "install_synthetic", auth.UpsertExternalAPIKeyParams{
		Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, FederationConfig: federationConfiguration(),
	})
	require.NoError(t, err)
	require.NotNil(t, repo.created.FederationConfig)
	assert.Equal(t, auth.AnthropicFederationAudience, repo.created.FederationConfig.Audience)
	assert.Empty(t, repo.created.KeyCiphertext)
	assert.NotNil(t, repo.created.KeyCiphertext)
	assert.Empty(t, repo.created.KeyFingerprint)
}

func TestFederationValidationRejectsSecretsAndWrongProviders(t *testing.T) {
	svc := makeServiceWithExternalKeys(t, &recordingExternalKeyRepo{})
	_, err := svc.UpsertExternalAPIKey(context.Background(), "install_synthetic", auth.UpsertExternalAPIKeyParams{
		Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, FederationConfig: federationConfiguration(), RawKey: "synthetic-accidental-secret",
	})
	require.ErrorIs(t, err, auth.ErrInvalidAnthropicFederation)
	for _, provider := range []string{providers.ProviderOpenAI, providers.ProviderAnthropicGateway} {
		_, err = auth.NormalizeAnthropicFederation(auth.AuthTypeAnthropicWIF, provider, "", federationConfiguration())
		require.ErrorIs(t, err, auth.ErrInvalidAnthropicFederation)
	}
	_, err = auth.NormalizeAnthropicFederation(auth.AuthTypeBearer, providers.ProviderAnthropic, "", federationConfiguration())
	require.ErrorIs(t, err, auth.ErrInvalidAnthropicFederation)
	_, err = auth.NormalizeAnthropicFederation(auth.AuthTypeAnthropicWIF, providers.ProviderAnthropic, "https://untrusted.example.com", federationConfiguration())
	require.ErrorIs(t, err, auth.ErrInvalidAnthropicFederation)
}

func TestFederationFailureCannotFallBackToDeploymentCredentials(t *testing.T) {
	key := &auth.ExternalAPIKey{ID: "federation-synthetic", InstallationID: "install_synthetic", Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, FederationConfig: federationConfiguration()}
	installation := &auth.Installation{ID: key.InstallationID, ExternalID: "org_synthetic", ByokEnabled: true}
	rawKey := "rk_synthetic_federation_test"
	apiKey := &auth.APIKey{ID: "key_synthetic", InstallationID: installation.ID, ExternalID: installation.ExternalID, KeyHash: auth.HashAPIKeySHA256(rawKey)}
	svc := makeServiceWithExternalKeys(t, &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{key}}, fakeKeyRow{apiKey: apiKey, installation: installation}).WithAnthropicFederationTokenSource(federationSourceFunc(func(context.Context, *auth.ExternalAPIKey) ([]byte, error) { return nil, errors.New("exchange failed") }))
	for range 2 {
		_, _, credentials, _, err := svc.VerifyAPIKey(context.Background(), rawKey)
		require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable, "both cold and cached authentication must fail closed")
		assert.Empty(t, credentials)
	}
}

func TestFederationCredentialResolutionKeepsCacheAndEndpointIsolated(t *testing.T) {
	key := &auth.ExternalAPIKey{ID: "federation-synthetic", InstallationID: "install_synthetic", Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, FederationConfig: federationConfiguration()}
	svc := makeServiceWithExternalKeys(t, &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{key}}).WithAnthropicFederationTokenSource(federationSourceFunc(func(context.Context, *auth.ExternalAPIKey) ([]byte, error) {
		return []byte("sk-ant-oat01-synthetic"), nil
	}))
	resolved, err := svc.ExternalAPIKeyWithCredential(context.Background(), key.InstallationID, key.ID)
	require.NoError(t, err)
	assert.Equal(t, auth.AnthropicFederationAudience, resolved.BaseURL)
	assert.Equal(t, "sk-ant-oat01-synthetic", string(resolved.Plaintext))
	assert.Empty(t, key.Plaintext, "shared auth cache must not retain a resolved access token")
	assert.Empty(t, key.BaseURL)
}

func TestDelegatedSessionPropagatesFederationFailure(t *testing.T) {
	fixture := newSessionIssuerFixture(t)
	_, keys := fixture.service(t)
	external := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{{ID: "ext-federation", InstallationID: fixture.inst.ID, Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, FederationConfig: federationConfiguration()}}}
	svc := auth.NewService(&fakeInstallationRepository{}, keys, external, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return sessionClock })
	token := fixture.sign(t, fixture.claims(), nil)
	_, _, _, _, _, err := svc.VerifySessionToken(context.Background(), token, true)
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
	_, _, _, _, _, err = svc.VerifySessionToken(context.Background(), token, false)
	require.NoError(t, err, "control-plane authentication does not need an upstream token")
}
