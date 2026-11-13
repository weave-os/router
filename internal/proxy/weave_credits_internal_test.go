package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deniedCtxWithKeys(keys ...*auth.ExternalAPIKey) context.Context {
	ctx := billing.WithWeaveCreditsDenied(context.Background())
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, keys)
	return withWeaveCreditsFunding(ctx, http.Header{})
}

func TestWeaveCreditsFunding_FailedKeyIsNotAFundingSource(t *testing.T) {
	ctx := deniedCtxWithKeys(
		&auth.ExternalAPIKey{Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok")},
		&auth.ExternalAPIKey{Provider: providers.ProviderOpenAI, CredentialError: errors.New("derive failed")},
	)
	enabled := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}}

	got, err := applyWeaveCreditsPolicy(ctx, enabled)

	require.NoError(t, err)
	assert.Equal(t, map[string]struct{}{providers.ProviderAnthropic: {}}, got)
	assert.False(t, customerFunded(BuildCredentialsMap(externalKeysFromContext(ctx))[providers.ProviderOpenAI]))
}

func TestWeaveCreditsFunding_AllowedLeavesProvidersAlone(t *testing.T) {
	enabled := map[string]struct{}{providers.ProviderOpenAI: {}}
	ctx := withWeaveCreditsFunding(context.Background(), http.Header{})

	got, err := applyWeaveCreditsPolicy(ctx, enabled)

	require.NoError(t, err)
	assert.Equal(t, enabled, got)
	assert.Nil(t, CredentialsFromContext(withPaidFallbackCredentials(ctx, providers.ProviderOpenAI)))
}

func TestPaidFallbackCredentials_UsesCustomerKeyWhenCreditsDenied(t *testing.T) {
	ctx := deniedCtxWithKeys(&auth.ExternalAPIKey{Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok")})

	creds := CredentialsFromContext(withPaidFallbackCredentials(ctx, providers.ProviderAnthropic))

	require.NotNil(t, creds)
	assert.True(t, customerFunded(creds))
	assert.Nil(t, CredentialsFromContext(withPaidFallbackCredentials(ctx, providers.ProviderOpenAI)))
}

func TestForcedModelBinding_UnfundedProviderReturnsCreditsError(t *testing.T) {
	svc := NewService(nil, map[string]providers.Client{}, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	ctx := deniedCtxWithKeys(&auth.ExternalAPIKey{Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok")})

	_, reason := svc.forcedModelBinding(ctx, "gpt-5.4", providers.ProviderOpenAI)
	binding, fundedReason := svc.forcedModelBinding(ctx, "claude-sonnet-4-6", providers.ProviderAnthropic)

	require.Equal(t, forcedModelUnfundedReason, reason)
	err := error(&ForcedModelExcludedError{Model: "gpt-5.4", Reason: reason})
	assert.ErrorIs(t, err, ErrWeaveCreditsDisabled)
	class, ok := ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusPaymentRequired, class.Status)
	assert.Empty(t, fundedReason)
	assert.Equal(t, providers.ProviderAnthropic, binding)
}

func TestOpenAIPaidCredential_IgnoresDeploymentKeyWhenCreditsDenied(t *testing.T) {
	svc := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: &betaCaptureProvider{}}, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	assert.True(t, svc.openAIPaidCredentialAvailable(context.Background(), http.Header{}))
	assert.False(t, svc.openAIPaidCredentialAvailable(deniedCtxWithKeys(), http.Header{}))
	assert.True(t, svc.openAIPaidCredentialAvailable(deniedCtxWithKeys(&auth.ExternalAPIKey{Provider: providers.ProviderOpenAI, Plaintext: []byte("synthetic-byok")}), http.Header{}))
}
