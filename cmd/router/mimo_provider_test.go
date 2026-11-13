package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

func TestUpstreamIDsForProvider_MiMoV26Flash(t *testing.T) {
	require.Equal(t, "XiaomiMiMo/MiMo-V2.6-Flash-RL", upstreamIDsForProvider(providers.ProviderMakora)[catalog.ModelMiMoV26Flash])
	require.Equal(t, "XiaomiMiMo/MiMo-V2.6-Flash", upstreamIDsForProvider(providers.ProviderDeepInfra)[catalog.ModelMiMoV26Flash])
}
