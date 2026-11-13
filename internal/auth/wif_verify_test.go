package auth_test

import (
	"context"
	"errors"
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubWIFSource struct {
	credential []byte
	err        error
	calls      int
}

func (s *stubWIFSource) Attestation(ctx context.Context) ([]byte, error) {
	s.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.credential, s.err
}

func wifKeyFor(installationID string) *auth.ExternalAPIKey {
	return &auth.ExternalAPIKey{
		ID:             "ext-key-wif",
		InstallationID: installationID,
		Provider:       "anthropic_gateway",
		KeyFingerprint: "fp-wif",
		AuthType:       auth.AuthTypeWIF,
	}
}

func wifVerifyFixture(t *testing.T, rawToken string, src auth.WIFTokenSource) *auth.Service {
	t.Helper()
	install := &auth.Installation{ID: "install_wif", ExternalID: "org_wif", Name: "wif-tenant"}
	apiKey := &auth.APIKey{
		ID:             "key_wif",
		InstallationID: install.ID,
		ExternalID:     install.ExternalID,
		KeyHash:        auth.HashAPIKeySHA256(rawToken),
	}
	external := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{wifKeyFor(install.ID)}}
	return makeServiceWithExternalKeys(t, external, fakeKeyRow{apiKey: apiKey, installation: install}).
		WithWIFTokenSource(src)
}

func TestService_VerifyAPIKey_AttachesWIFAttestation(t *testing.T) {
	rawToken := "rk_wif_attest_test"
	src := &stubWIFSource{credential: []byte("WIF.GCP.header.payload.sig")}
	svc := wifVerifyFixture(t, rawToken, src)

	_, _, externalKeys, _, err := svc.VerifyAPIKey(context.Background(), rawToken)

	require.NoError(t, err)
	require.Len(t, externalKeys, 1)
	assert.Equal(t, "WIF.GCP.header.payload.sig", string(externalKeys[0].Plaintext),
		"a WIF row stores no secret, so the attestation is the only credential the proxy can dispatch with")
	assert.Equal(t, 1, src.calls)
}

func TestService_VerifyAPIKey_DropsWIFKeyWhenAttestationFails(t *testing.T) {
	rawToken := "rk_wif_broken_test"
	svc := wifVerifyFixture(t, rawToken, &stubWIFSource{err: errors.New("metadata server unreachable")})

	_, _, externalKeys, _, err := svc.VerifyAPIKey(context.Background(), rawToken)

	require.NoError(t, err, "an unattestable BYOK row must not fail authentication for the whole request")
	assert.Empty(t, externalKeys,
		"without an attestation there is no credential at all — dispatching would send an empty bearer")
}

func TestService_VerifyAPIKey_DropsWIFKeyWhenNoSourceIsWired(t *testing.T) {
	rawToken := "rk_wif_unwired_test"
	install := &auth.Installation{ID: "install_wif", ExternalID: "org_wif", Name: "wif-tenant"}
	apiKey := &auth.APIKey{
		ID:             "key_wif",
		InstallationID: install.ID,
		ExternalID:     install.ExternalID,
		KeyHash:        auth.HashAPIKeySHA256(rawToken),
	}
	external := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{wifKeyFor(install.ID)}}
	svc := makeServiceWithExternalKeys(t, external, fakeKeyRow{apiKey: apiKey, installation: install})

	_, _, externalKeys, _, err := svc.VerifyAPIKey(context.Background(), rawToken)

	require.NoError(t, err)
	assert.Empty(t, externalKeys,
		"a deployment with no attestation source must drop the key rather than dispatch unauthenticated")
}

func TestService_UpsertExternalAPIKey_RejectsWIFForVendorProvider(t *testing.T) {
	svc := makeServiceWithExternalKeys(t, &fakeExternalAPIKeyRepo{})

	_, err := svc.UpsertExternalAPIKey(context.Background(), "install_wif", auth.UpsertExternalAPIKeyParams{
		Provider: "anthropic",
		AuthType: auth.AuthTypeWIF,
	})

	require.ErrorIs(t, err, auth.ErrInvalidKeypairAuth,
		"workload identity is a gateway credential shape; vendor providers authenticate with their own API keys")
}

func TestService_UpsertExternalAPIKey_RejectsWIFWithKeyMaterial(t *testing.T) {
	baseURL := "https://acct.example.com/api/v2/cortex/v1"
	svc := makeServiceWithExternalKeys(t, &fakeExternalAPIKeyRepo{})

	_, err := svc.UpsertExternalAPIKey(context.Background(), "install_wif", auth.UpsertExternalAPIKeyParams{
		Provider: "anthropic_gateway",
		RawKey:   "sk-pasted-by-mistake",
		BaseURL:  &baseURL,
		AuthType: auth.AuthTypeWIF,
	})

	require.ErrorIs(t, err, auth.ErrInvalidKeypairAuth,
		"a stored secret under WIF would never be used, so accepting it hides a misconfiguration")
}

// recordingExternalKeyRepo captures what a create would persist; the shared fake
// discards its params.
type recordingExternalKeyRepo struct {
	fakeExternalAPIKeyRepo
	created auth.CreateExternalAPIKeyParams
}

func (r *recordingExternalKeyRepo) Create(ctx context.Context, params auth.CreateExternalAPIKeyParams) (*auth.ExternalAPIKey, error) {
	r.created = params
	return &auth.ExternalAPIKey{ID: params.ExternalID, Provider: params.Provider, AuthType: params.AuthType}, nil
}

func TestService_UpsertExternalAPIKey_StoresWIFKeyWithoutSecret(t *testing.T) {
	baseURL := "https://acct.example.com/api/v2/cortex/v1"
	repo := &recordingExternalKeyRepo{}
	svc := makeServiceWithExternalKeys(t, repo)

	key, err := svc.UpsertExternalAPIKey(context.Background(), "install_wif", auth.UpsertExternalAPIKeyParams{
		Provider: "anthropic_gateway",
		BaseURL:  &baseURL,
		AuthType: auth.AuthTypeWIF,
	})

	require.NoError(t, err)
	assert.Equal(t, auth.AuthTypeWIF, key.AuthType)
	assert.Nil(t, repo.created.AuthAccount, "the principal lives in the attestation, not in the stored row")
	assert.Nil(t, repo.created.AuthUser)
	assert.Empty(t, repo.created.KeyPrefix, "there is no secret to display a prefix of")
}
