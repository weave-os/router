package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func forceableByID(models []forceableModel) map[string]forceableModel {
	out := make(map[string]forceableModel, len(models))
	for _, m := range models {
		out[m.id] = m
	}
	return out
}

// The listing is judged by the same check /force-model applies, so a model the
// installation's allowlist refuses is listed but marked off.
func TestForceableModels_MarksModelsTheForceCheckRefuses(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))
	ctx := context.WithValue(context.Background(),
		InstallationAllowedModelsContextKey{}, []string{"claude-opus-4-8"})

	models := forceableByID(svc.forceableModels(ctx))

	require.Contains(t, models, "claude-opus-4-8")
	assert.Empty(t, models["claude-opus-4-8"].refusal)
	require.Contains(t, models, "claude-opus-5-5")
	assert.Contains(t, models["claude-opus-5-5"].refusal, "allowed-model list")
}

// Every row is a name /force-model resolves to itself, and each shorthand shown
// resolves to that row's model.
func TestForceableModels_ShorthandsResolveToTheirRow(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	models := svc.forceableModels(context.Background())
	require.NotEmpty(t, models)
	for _, m := range models {
		id, _, known := resolveForceModel(m.id)
		require.True(t, known, "%s is listed but /force-model does not know it", m.id)
		assert.Equal(t, m.id, id)
		for _, name := range m.shorthands {
			resolved, _, known := resolveForceModel(name)
			assert.True(t, known, "shorthand %q for %s does not resolve", name, m.id)
			assert.Equal(t, m.id, resolved, "shorthand %q resolves elsewhere", name)
		}
	}
	assert.Contains(t, forceableByID(models)[forceModelAliases["opus"]].shorthands, "opus")
}

func TestListModelsMessage_MarksRefusedRowsAndCountsForceable(t *testing.T) {
	msg := listModelsMessage([]forceableModel{
		{id: "claude-opus-5-5", provider: providers.ProviderAnthropic, shorthands: []string{"opus", "claude"}},
		{id: "gpt-6.1-sol", provider: providers.ProviderOpenAI, refusal: "excluded", shorthands: []string{"sol"}},
	}, "$", translate.FormatOpenAI)

	assert.Contains(t, msg, "[x] claude-opus-5-5 (opus, claude)")
	assert.Contains(t, msg, "[ ] gpt-6.1-sol (sol)")
	assert.Contains(t, msg, "1 of 2 can be forced here")
	assert.Contains(t, msg, "refused by this installation")
}

func TestListModelsMessage_UsesTheClientsOwnSigil(t *testing.T) {
	models := []forceableModel{{id: "claude-opus-5-5", provider: providers.ProviderAnthropic}}

	codex := listModelsMessage(models, "$", translate.FormatOpenAI)
	assert.Contains(t, codex, "`$force-model <id or shorthand>`")
	assert.NotContains(t, codex, "refused by this installation")

	claude := listModelsMessage(models, "/", translate.FormatAnthropic)
	assert.Contains(t, claude, "`/force-model <id or shorthand>`")
	assert.Contains(t, claude, routingMarkerPrefix)
}

func TestListModelsMessage_CapsShorthandsPerRow(t *testing.T) {
	msg := listModelsMessage([]forceableModel{{
		id: "claude-opus-5-5", provider: providers.ProviderAnthropic,
		shorthands: []string{"opus", "claude", "opus55", "opus-5.5", "opus-5-5"},
	}}, "$", translate.FormatOpenAI)

	assert.Contains(t, msg, "(opus, claude, opus55, opus-5.5)")
	assert.NotContains(t, msg, "opus-5-5)")
}
