package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func forceableByID(models []conversation.ForceableModel) map[string]conversation.ForceableModel {
	out := make(map[string]conversation.ForceableModel, len(models))
	for _, m := range models {
		out[m.ID] = m
	}
	return out
}

func forceableModels(ctx context.Context, svc *Service) []conversation.ForceableModel {
	return conversation.ListForceableModels(ctx, forceModelNames(), forceModelResolver{svc})
}

// The listing is judged by the same check /force-model applies, so a model the
// installation's allowlist refuses is listed but marked off.
func TestForceableModels_MarksModelsTheForceCheckRefuses(t *testing.T) {
	svc := withHMMPorts(NewService(nil, dispatch.NewClients(nil), nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))
	ctx := context.WithValue(context.Background(),
		requestcontext.InstallationAllowedModelsContextKey{}, []string{"claude-opus-4-8"})

	models := forceableByID(forceableModels(ctx, svc))

	require.Contains(t, models, "claude-opus-4-8")
	assert.False(t, models["claude-opus-4-8"].Refused)
	require.Contains(t, models, "claude-opus-5-5")
	require.True(t, models["claude-opus-5-5"].Refused)
	assert.Contains(t, models["claude-opus-5-5"].RefusalReason, "allowed-model list")
}

// Every row is a name /force-model resolves to itself, and each shorthand shown
// resolves to that row's model.
func TestForceableModels_ShorthandsResolveToTheirRow(t *testing.T) {
	svc := withHMMPorts(NewService(nil, dispatch.NewClients(nil), nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil))

	models := forceableModels(context.Background(), svc)
	require.NotEmpty(t, models)
	for _, m := range models {
		id, _, known := resolveForceModel(m.ID)
		require.True(t, known, "%s is listed but /force-model does not know it", m.ID)
		assert.Equal(t, m.ID, id)
		for _, name := range m.Shorthands {
			resolved, _, known := resolveForceModel(name)
			assert.True(t, known, "shorthand %q for %s does not resolve", name, m.ID)
			assert.Equal(t, m.ID, resolved, "shorthand %q resolves elsewhere", name)
		}
	}
	assert.Contains(t, forceableByID(models)[forceModelAliases["opus"]].Shorthands, "opus")
}
