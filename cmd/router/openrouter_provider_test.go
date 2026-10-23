package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenRouterModelIDs_SeedsCatalogUpstreamIDs(t *testing.T) {
	ids, err := openRouterModelIDs("")
	require.NoError(t, err)

	assert.Equal(t, "moonshotai/kimi-k3.1-pro", ids["moonshotai/kimi-k3.1"])
}

func TestOpenRouterModelIDs_EnvOverridesCatalog(t *testing.T) {
	ids, err := openRouterModelIDs("moonshotai/kimi-k3.1=custom/kimi,vendor/x=vendor/x-v2")
	require.NoError(t, err)

	assert.Equal(t, "custom/kimi", ids["moonshotai/kimi-k3.1"])
	assert.Equal(t, "vendor/x-v2", ids["vendor/x"])
}

func TestOpenRouterModelIDs_RejectsInvalidEnv(t *testing.T) {
	_, err := openRouterModelIDs("missing-separator")
	require.Error(t, err)
}
