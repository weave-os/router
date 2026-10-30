package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
)

type federationKeyRepo struct {
	fakeExternalAPIKeyRepo
	params auth.CreateExternalAPIKeyParams
}

func (r *federationKeyRepo) Create(_ context.Context, p auth.CreateExternalAPIKeyParams) (*auth.ExternalAPIKey, error) {
	r.params = p
	r.created++
	return &auth.ExternalAPIKey{ID: p.ExternalID, Provider: p.Provider, AuthType: p.AuthType, FederationRuleID: *p.FederationRuleID, OrganizationID: *p.OrganizationID, ServiceAccountID: *p.ServiceAccountID}, nil
}

func TestUpsertAnthropicWIFAlongsideDeploymentKey(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "deployment-api-key")
	repo := &federationKeyRepo{}
	body, err := json.Marshal(map[string]string{"provider": providers.ProviderAnthropic, "auth_type": auth.AuthTypeAnthropicWIF, "federation_rule_id": "fdrl_test", "organization_id": "test-org", "service_account_id": "svac_test"})
	require.NoError(t, err)
	rec := postProviderKeyBody(upsertKeyEngine(newUpsertKeyService(repo)), body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, 1, repo.created)
	assert.Empty(t, repo.params.KeyCiphertext)
	var saved map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &saved))
	assert.Equal(t, auth.AuthTypeAnthropicWIF, saved["auth_type"])
	assert.Equal(t, "fdrl_test", saved["federation_rule_id"])
	assert.Equal(t, "test-org", saved["organization_id"])
	assert.Equal(t, "svac_test", saved["service_account_id"])
	assert.NotContains(t, saved, "key")
	assert.NotContains(t, saved, "access_token")
}

func TestUpsertAnthropicWIFRejectsInvalidConfigurationBeforeReplacement(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "")
	for _, changes := range []map[string]string{
		{"federation_rule_id": ""}, {"organization_id": ""}, {"service_account_id": ""},
		{"provider": providers.ProviderOpenAI}, {"key": "unexpected-secret"}, {"base_url": "https://custom.example.com"}, {"auth_account": "account"},
	} {
		payload := map[string]string{"provider": providers.ProviderAnthropic, "auth_type": auth.AuthTypeAnthropicWIF, "federation_rule_id": "fdrl_test", "organization_id": "test-org", "service_account_id": "svac_test"}
		for field, value := range changes {
			payload[field] = value
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		repo := &fakeExternalAPIKeyRepo{}
		rec := postProviderKeyBody(upsertKeyEngine(newUpsertKeyService(repo)), body)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Zero(t, repo.created)
		assert.Zero(t, repo.softDeletedByProvider)
	}
}
