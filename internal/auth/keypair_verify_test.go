package auth_test

import (
	"context"
	"strings"
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_VerifyAPIKey_MintsKeypairToken(t *testing.T) {
	rawToken := "rk_keypair_mint_test"
	install := &auth.Installation{ID: "install_kp", ExternalID: "org_kp", Name: "kp-tenant"}
	apiKey := &auth.APIKey{
		ID:             "key_kp",
		InstallationID: install.ID,
		ExternalID:     install.ExternalID,
		KeyHash:        auth.HashAPIKeySHA256(rawToken),
	}
	stored := keypairKeyFor(t, "ext-key-kp", "fp-1")
	stored.InstallationID = install.ID
	fakeExternal := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{stored}}
	svc := makeServiceWithExternalKeys(t, fakeExternal, fakeKeyRow{apiKey: apiKey, installation: install})

	_, _, externalKeys, _, err := svc.VerifyAPIKey(context.Background(), rawToken)

	require.NoError(t, err)
	require.Len(t, externalKeys, 1)
	claims := decodeClaims(t, string(externalKeys[0].Plaintext))
	assert.Equal(t, "MYORG-MYACCOUNT.SERVICE_USER", claims["sub"],
		"a key-pair row must reach the proxy as a signed token, not as the private key it stores")
	assert.True(t, strings.HasPrefix(string(stored.Plaintext), "-----BEGIN"),
		"minting must not overwrite the private key on the cached row it was signed from")
}

func TestService_UpsertExternalAPIKey_RejectsKeypairForVendorProvider(t *testing.T) {
	account, user := "MYORG-MYACCOUNT", "SERVICE_USER"
	svc := makeServiceWithExternalKeys(t, &fakeExternalAPIKeyRepo{})

	_, err := svc.UpsertExternalAPIKey(context.Background(), "install_kp", auth.UpsertExternalAPIKeyParams{
		Provider:    "anthropic",
		RawKey:      string(pkcs8PEM(t, testKey)),
		AuthType:    auth.AuthTypeKeypairJWT,
		AuthAccount: &account,
		AuthUser:    &user,
	})

	require.ErrorIs(t, err, auth.ErrInvalidKeypairAuth,
		"key-pair auth is a gateway credential shape; vendor providers authenticate with their own API keys")
}

func TestService_UpsertExternalAPIKey_RejectsKeypairSecretThatIsNotAKey(t *testing.T) {
	account, user := "MYORG-MYACCOUNT", "SERVICE_USER"
	baseURL := "https://acct.example.com/api/v2/cortex/v1"
	svc := makeServiceWithExternalKeys(t, &fakeExternalAPIKeyRepo{})

	_, err := svc.UpsertExternalAPIKey(context.Background(), "install_kp", auth.UpsertExternalAPIKeyParams{
		Provider:    "anthropic_gateway",
		RawKey:      "sk-not-a-private-key",
		BaseURL:     &baseURL,
		AuthType:    auth.AuthTypeKeypairJWT,
		AuthAccount: &account,
		AuthUser:    &user,
	})

	require.ErrorIs(t, err, auth.ErrInvalidKeypairAuth,
		"a pasted secret that cannot sign must fail at configuration time, where the operator can still fix it")
}

func TestService_VerifyAPIKey_DropsKeypairKeyThatCannotMint(t *testing.T) {
	rawToken := "rk_keypair_broken_test"
	install := &auth.Installation{ID: "install_kpx", ExternalID: "org_kpx", Name: "kpx-tenant"}
	apiKey := &auth.APIKey{
		ID:             "key_kpx",
		InstallationID: install.ID,
		ExternalID:     install.ExternalID,
		KeyHash:        auth.HashAPIKeySHA256(rawToken),
	}
	broken := keypairKeyFor(t, "ext-key-kpx", "fp-1")
	broken.Plaintext = []byte("not-a-private-key")
	fakeExternal := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{broken}}
	svc := makeServiceWithExternalKeys(t, fakeExternal, fakeKeyRow{apiKey: apiKey, installation: install})

	_, _, externalKeys, _, err := svc.VerifyAPIKey(context.Background(), rawToken)

	require.NoError(t, err, "an unmintable BYOK row must not fail authentication for the whole request")
	assert.Empty(t, externalKeys,
		"a key whose token cannot be minted must be dropped so routing never dispatches with a raw private key")
}
