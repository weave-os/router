package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deniedCtxWithKeys(keys ...*auth.ExternalAPIKey) context.Context {
	ctx := billing.WithWeaveCreditsDenied(context.Background())
	ctx = context.WithValue(ctx, credentials.ExternalAPIKeysContextKey{}, keys)
	return credentials.WithWeaveCreditsFunding(ctx, http.Header{})
}

func TestWeaveCreditsFunding_FailedKeyIsNotAFundingSource(t *testing.T) {
	ctx := deniedCtxWithKeys(
		&auth.ExternalAPIKey{Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok")},
		&auth.ExternalAPIKey{Provider: providers.ProviderOpenAI, CredentialError: errors.New("derive failed")},
	)
	enabled := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}}

	got, err := credentials.ApplyWeaveCreditsPolicy(ctx, enabled)

	require.NoError(t, err)
	assert.Equal(t, map[string]struct{}{providers.ProviderAnthropic: {}}, got)
	assert.False(t, credentials.CustomerFunded(credentials.BuildCredentialsMap(credentials.ExternalKeysFromContext(ctx))[providers.ProviderOpenAI]))
}

func TestPaidFallbackCredentials_UsesCustomerKeyWhenCreditsDenied(t *testing.T) {
	ctx := deniedCtxWithKeys(&auth.ExternalAPIKey{Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok")})

	creds := credentials.CredentialsFromContext(credentials.WithPaidFallbackCredentials(ctx, providers.ProviderAnthropic))

	require.NotNil(t, creds)
	assert.True(t, credentials.CustomerFunded(creds))
	assert.Nil(t, credentials.CredentialsFromContext(credentials.WithPaidFallbackCredentials(ctx, providers.ProviderOpenAI)))
}

func TestForcedModelBinding_UnfundedProviderReturnsCreditsError(t *testing.T) {
	svc := withHMMPorts(NewService(nil, dispatch.NewClients(map[string]providers.Client{}), nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil))
	ctx := deniedCtxWithKeys(&auth.ExternalAPIKey{Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok")})

	_, refusal := svc.forcedModelBinding(ctx, "gpt-5.4", providers.ProviderOpenAI)
	binding, fundedRefusal := svc.forcedModelBinding(ctx, "claude-sonnet-4-6", providers.ProviderAnthropic)

	require.NotNil(t, refusal)
	require.Equal(t, credentials.ForcedModelUnfundedReason, refusal.Reason)
	require.Equal(t, "gpt-5.4", refusal.Model)
	err := error(refusal)
	require.ErrorIs(t, err, credentials.ErrWeaveCreditsDisabled)
	class, ok := ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusPaymentRequired, class.Status)
	assert.Nil(t, fundedRefusal)
	assert.Equal(t, providers.ProviderAnthropic, binding)
}

func TestOpenAIPaidCredential_IgnoresDeploymentKeyWhenCreditsDenied(t *testing.T) {
	svc := withHMMPorts(NewService(nil, dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: &captureTestProvider{}}), nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil))

	assert.True(t, svc.openAIPaidCredentialAvailable(context.Background(), http.Header{}))
	assert.False(t, svc.openAIPaidCredentialAvailable(deniedCtxWithKeys(), http.Header{}))
	assert.True(t, svc.openAIPaidCredentialAvailable(deniedCtxWithKeys(&auth.ExternalAPIKey{Provider: providers.ProviderOpenAI, Plaintext: []byte("synthetic-byok")}), http.Header{}))
}
