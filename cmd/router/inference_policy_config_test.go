package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/policy"
)

func TestResolveCompactionModelFailsClosed(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("ROUTER_COMPACTION_MODEL", "")
		model, err := resolveCompactionModel(providers.ProviderAnthropic)
		require.NoError(t, err)
		assert.Equal(t, policy.PrecompactionDefaultModel, model)
	})

	t.Run("valid Anthropic binding", func(t *testing.T) {
		t.Setenv("ROUTER_COMPACTION_MODEL", "claude-fable-5")
		model, err := resolveCompactionModel(providers.ProviderAnthropic)
		require.NoError(t, err)
		assert.Equal(t, "claude-fable-5", model)
	})

	t.Run("invalid binding", func(t *testing.T) {
		t.Setenv("ROUTER_COMPACTION_MODEL", "gpt-5.5")
		_, err := resolveCompactionModel(providers.ProviderAnthropic)
		assert.ErrorContains(t, err, "has no anthropic catalog binding")
	})

	t.Run("binding on the configured summarizer provider", func(t *testing.T) {
		t.Setenv("ROUTER_COMPACTION_MODEL", "claude-fable-5")
		_, err := resolveCompactionModel(providers.ProviderOpenAI)
		assert.ErrorContains(t, err, "has no openai catalog binding")
	})
}

func TestResolveHardPinModelRejectsProviderWithoutModel(t *testing.T) {
	t.Setenv("ROUTER_HARD_PIN_MODEL", "")
	t.Setenv("ROUTER_HARD_PIN_PROVIDER", providers.ProviderOpenAI)

	_, _, err := resolveHardPinModel(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.ErrorContains(t, err, "requires ROUTER_HARD_PIN_MODEL")
}

func TestResolveHardPinModelUsesDefaultProviderForExplicitModel(t *testing.T) {
	t.Setenv("ROUTER_HARD_PIN_MODEL", "claude-haiku-4-5")
	t.Setenv("ROUTER_HARD_PIN_PROVIDER", "")

	provider, model, err := resolveHardPinModel(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderAnthropic, provider)
	assert.Equal(t, "claude-haiku-4-5", model)
}
