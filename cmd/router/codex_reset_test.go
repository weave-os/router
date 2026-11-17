package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	openaiProvider "weave-os/router/internal/providers/openai"
)

func TestCodexResetClientUsesOpenAIAdapter(t *testing.T) {
	require.NotNil(t, codexResetClient(map[string]providers.Client{providers.ProviderOpenAI: openaiProvider.NewClient("", "")}))
	require.Nil(t, codexResetClient(map[string]providers.Client{}))
}

func TestFixedFlagDefaultsKeepCodexResetsOff(t *testing.T) {
	require.Equal(t, "false", fixedFlagDefaults[flags.KeyCodexAutoUsageReset])
}
