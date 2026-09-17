package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func TestDeepSeekUpstreamIDs(t *testing.T) {
	ids := upstreamIDsForProvider(providers.ProviderDeepSeek)
	assert.Equal(t, "deepseek-flash", ids["deepseek/deepseek-v4-flash"])
	assert.Equal(t, "deepseek-v4-pro", ids["deepseek/deepseek-v4-pro"])
	assert.Equal(t, "deepseek-v4-pro", ids["deepseek/deepseek-v4-pro-0813"])
}
