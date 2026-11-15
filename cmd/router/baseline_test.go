package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveDefaultBaselineModel(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "")
		require.NoError(t, os.Unsetenv("ROUTER_DEFAULT_BASELINE_MODEL"))
		assert.Equal(t, "claude-sonnet-4-5", resolveDefaultBaselineModel())
	})

	t.Run("explicit empty disables substitution", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "")
		assert.Equal(t, "", resolveDefaultBaselineModel())
	})

	t.Run("explicit value wins", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "claude-opus-4-7")
		assert.Equal(t, "claude-opus-4-7", resolveDefaultBaselineModel())
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "  claude-haiku-4-5  ")
		assert.Equal(t, "claude-haiku-4-5", resolveDefaultBaselineModel())
	})
}
