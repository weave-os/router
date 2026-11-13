package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

func TestNormalizeAuthTypeAzureEntraPreservesPrincipalValues(t *testing.T) {
	tenantID := "  12345678-1234-1234-1234-123456789abc  "
	clientID := "  ABCDEFAB-1234-1234-1234-ABCDEFABCDEF  "

	authType, gotTenantID, gotClientID, err := auth.NormalizeAuthType(
		auth.AuthTypeAzureEntra,
		&tenantID,
		&clientID,
	)

	require.NoError(t, err)
	assert.Equal(t, auth.AuthTypeAzureEntra, authType)
	assert.Equal(t, "12345678-1234-1234-1234-123456789abc", *gotTenantID)
	assert.Equal(t, "ABCDEFAB-1234-1234-1234-ABCDEFABCDEF", *gotClientID)
}

func TestEntraScopeForBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name, baseURL, want string
	}{
		{name: "empty base URL", want: auth.EntraScope},
		{name: "foundry", baseURL: "https://resource.services.ai.azure.com/anthropic", want: auth.EntraScope},
		{name: "azure openai", baseURL: " https://resource.OpenAI.Azure.com/openai/v1 ", want: auth.EntraScopeCognitiveServices},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, auth.EntraScopeForBaseURL(tc.baseURL))
		})
	}
}

func TestNormalizeAuthTypeAzureEntraRequiresTenantAndClient(t *testing.T) {
	value := "value"
	for _, tc := range []struct {
		name          string
		account, user *string
	}{
		{name: "missing both"},
		{name: "missing tenant", user: &value},
		{name: "missing client", account: &value},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := auth.NormalizeAuthType(auth.AuthTypeAzureEntra, tc.account, tc.user)
			require.Error(t, err)
			assert.ErrorIs(t, err, auth.ErrInvalidEntraAuth)
		})
	}
}
