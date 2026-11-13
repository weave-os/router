package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

type stubEntraSource struct {
	credential []byte
	err        error
	calls      int
}

func (s *stubEntraSource) Token(ctx context.Context, key *auth.ExternalAPIKey) ([]byte, error) {
	s.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.credential, s.err
}

func azureEntraKeyFor(installationID string) *auth.ExternalAPIKey {
	return &auth.ExternalAPIKey{
		ID:             "ext-key-entra",
		InstallationID: installationID,
		Provider:       "anthropic_gateway",
		KeyFingerprint: "fp-entra",
		AuthType:       auth.AuthTypeAzureEntra,
		AuthAccount:    "tenant-id",
		AuthUser:       "client-id",
		Plaintext:      []byte("client-secret"),
	}
}

func entraVerifyFixture(t *testing.T, rawToken string, src auth.EntraTokenSource) *auth.Service {
	t.Helper()
	install := &auth.Installation{ID: "install_entra", ExternalID: "org_entra", Name: "entra-tenant"}
	apiKey := &auth.APIKey{
		ID:             "key_entra",
		InstallationID: install.ID,
		ExternalID:     install.ExternalID,
		KeyHash:        auth.HashAPIKeySHA256(rawToken),
	}
	external := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{azureEntraKeyFor(install.ID)}}
	return makeServiceWithExternalKeys(t, external, fakeKeyRow{apiKey: apiKey, installation: install}).
		WithEntraTokenSource(src)
}

func TestService_VerifyAPIKeyAttachesEntraToken(t *testing.T) {
	src := &stubEntraSource{credential: []byte("entra-access-token")}
	svc := entraVerifyFixture(t, "rk_entra_attest_test", src)

	_, _, externalKeys, _, err := svc.VerifyAPIKey(context.Background(), "rk_entra_attest_test")

	require.NoError(t, err)
	require.Len(t, externalKeys, 1)
	assert.Equal(t, "entra-access-token", string(externalKeys[0].Plaintext))
	assert.Equal(t, 1, src.calls)
}

func TestService_VerifyAPIKeyDropsEntraKeyWhenTokenFails(t *testing.T) {
	svc := entraVerifyFixture(t, "rk_entra_broken_test", &stubEntraSource{err: errors.New("token endpoint unavailable")})

	_, _, externalKeys, _, err := svc.VerifyAPIKey(context.Background(), "rk_entra_broken_test")

	require.NoError(t, err)
	assert.Empty(t, externalKeys, "a failed token exchange must not send the client secret upstream")
}

func TestService_UpsertExternalAPIKeyRejectsEntraForVendorProvider(t *testing.T) {
	tenantID, clientID, baseURL := "tenant-id", "client-id", "https://example.com/anthropic"
	svc := makeServiceWithExternalKeys(t, &fakeExternalAPIKeyRepo{})

	_, err := svc.UpsertExternalAPIKey(context.Background(), "install_entra", auth.UpsertExternalAPIKeyParams{
		Provider:    "anthropic",
		RawKey:      "client-secret",
		BaseURL:     &baseURL,
		AuthType:    auth.AuthTypeAzureEntra,
		AuthAccount: &tenantID,
		AuthUser:    &clientID,
	})

	require.ErrorIs(t, err, auth.ErrInvalidEntraAuth)
}
