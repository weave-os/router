package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/server"
)

func TestManagedAnthropicFederationRequiresOperatorApproval(t *testing.T) {
	t.Setenv("ROUTER_ANTHROPIC_FEDERATION_BINDINGS_B64", "e30=")
	source := buildAnthropicFederationTokenSource(server.DeploymentModeManaged)
	_, err := source.Token(context.Background(), &auth.ExternalAPIKey{
		ID: "synthetic-key", InstallationID: "00000000-0000-0000-0000-000000000003",
		Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF,
		FederationConfig: &auth.AnthropicFederationConfig{FederationRuleID: "fdrl_synthetic", OrganizationID: "00000000-0000-0000-0000-000000000001", ServiceAccountID: "svac_synthetic"},
	})
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
	require.Contains(t, err.Error(), "not approved")
}

func TestManagedAnthropicFederationRejectsMalformedOperatorConfiguration(t *testing.T) {
	t.Setenv("ROUTER_ANTHROPIC_FEDERATION_BINDINGS_B64", "invalid-base64")
	require.Panics(t, func() { buildAnthropicFederationTokenSource(server.DeploymentModeManaged) })
}
