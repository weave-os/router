package anthropicfederation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/anthropicfederation"
	"weave-os/router/internal/auth"
)

func TestManagedBindingsPreventCrossInstallationFederation(t *testing.T) {
	key := federationKey()
	approved := *key.FederationConfig
	approved.Audience = auth.AnthropicFederationAudience
	bindings := map[string]auth.AnthropicFederationConfig{key.InstallationID: approved}
	calls := 0
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(http.StatusOK, tokenResponse), nil
	})}
	source := anthropicfederation.NewSource(client, func(context.Context, string) (string, error) { return assertion(""), nil }, time.Now).RequireInstallationBindings(bindings)
	_, err := source.Token(context.Background(), key)
	require.NoError(t, err)
	attacker := *key
	attacker.InstallationID = "different-installation"
	bindings[attacker.InstallationID] = approved
	_, err = source.Token(context.Background(), &attacker)
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable, "knowing a victim's federation settings does not authorize an installation")
	assert.Equal(t, 1, calls, "unapproved installations must not exchange or use another installation's cached token")
	for _, mutate := range []func(*auth.AnthropicFederationConfig){
		func(c *auth.AnthropicFederationConfig) { c.FederationRuleID = "fdrl_other" },
		func(c *auth.AnthropicFederationConfig) { c.OrganizationID = "00000000-0000-0000-0000-000000000002" },
		func(c *auth.AnthropicFederationConfig) { c.ServiceAccountID = "svac_other" },
		func(c *auth.AnthropicFederationConfig) { c.WorkspaceID = "wrkspc_other" },
		func(c *auth.AnthropicFederationConfig) { c.Audience = "other-audience" },
	} {
		changed := *key.FederationConfig
		mutate(&changed)
		candidate := *key
		candidate.FederationConfig = &changed
		_, err = source.Token(context.Background(), &candidate)
		require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
	}
	assert.Equal(t, 1, calls)
}

func TestManagedBindingsDefaultDeny(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unapproved request reached token endpoint")
		return response(http.StatusOK, tokenResponse), nil
	})}
	source := anthropicfederation.NewSource(client, func(context.Context, string) (string, error) {
		t.Error("unapproved request minted an assertion")
		return assertion(""), nil
	}, time.Now).RequireInstallationBindings(nil)
	_, err := source.Token(context.Background(), federationKey())
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
}

func TestParseInstallationBindingsNormalizesOperatorConfiguration(t *testing.T) {
	const installationID = "00000000-0000-0000-0000-000000000003"
	payload, _ := json.Marshal(map[string]auth.AnthropicFederationConfig{installationID: *federationKey().FederationConfig})
	bindings, err := anthropicfederation.ParseInstallationBindings(string(payload))
	require.NoError(t, err)
	assert.Equal(t, auth.AnthropicFederationAudience, bindings[installationID].Audience)
	for _, invalid := range []string{`bad json`, `{}`, `null`, `{} {}`, `{"not-an-installation":{}}`} {
		parsed, err := anthropicfederation.ParseInstallationBindings(invalid)
		if invalid == "{}" || invalid == "null" {
			require.NoError(t, err)
			assert.Empty(t, parsed)
		} else {
			require.Error(t, err)
		}
	}
}
