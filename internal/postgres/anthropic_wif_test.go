package postgres

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sqlc"
)

func TestExternalKeyMapsAnthropicFederationMetadata(t *testing.T) {
	rule, org, service, workspace := "fdrl_test", "test-org", "svac_test", "wrkspc_test"
	key, err := toExternalAPIKey(sqlc.RouterModelRouterExternalAPIKey{
		Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF,
		AnthropicFederationRuleID: &rule, AnthropicOrganizationID: &org, AnthropicServiceAccountID: &service, AnthropicWorkspaceID: &workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, rule, key.FederationRuleID)
	assert.Equal(t, org, key.OrganizationID)
	assert.Equal(t, service, key.ServiceAccountID)
	assert.Equal(t, workspace, key.WorkspaceID)
	assert.Empty(t, key.Plaintext)
}
