package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedServingRequiresExplicitAssertionKey(t *testing.T) {
	// Future deployment values must not opt an existing worker into new dependencies.
	t.Setenv("ROUTER_SERVING_REGISTRY_URI", "invalid-disabled-registry")
	t.Setenv("ROUTER_SERVING_TARGET", "invalid-disabled-target")
	t.Setenv("ROUTER_SERVING_CONFIGURATION_GENERATION", "invalid-disabled-generation")
	for _, key := range []string{"", " \t\n"} {
		t.Setenv("ROUTER_SERVING_ASSERTION_KEY", key)
		assert.False(t, managedServingEnabled())
	}
	t.Setenv("ROUTER_SERVING_ASSERTION_KEY", strings.Repeat("k", 32))
	assert.True(t, managedServingEnabled())
}

func TestManagedServingWeakOptInFailsBeforeDependencies(t *testing.T) {
	t.Setenv("ROUTER_SERVING_ASSERTION_KEY", "weak-key")
	t.Setenv("ROUTER_SERVING_TARGET", "")
	t.Setenv("ROUTER_SERVING_REGISTRY_URI", "invalid-registry")
	assert.True(t, managedServingEnabled())
	admission, snapshot, closeRegistry, err := buildManagedServingRuntime(context.Background(), nil)
	require.ErrorContains(t, err, "at least 32 key bytes")
	assert.Nil(t, admission)
	assert.Nil(t, snapshot)
	assert.Nil(t, closeRegistry)
}
