package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseEnvAttemptTimeoutMs(t *testing.T) {
	const key = "ROUTER_HMM_SIDECAR_ATTEMPT_TIMEOUT_MS"
	fallback := 1800 * time.Millisecond

	// 0 is how an operator gives a single attempt the whole decision budget.
	// Routing it through the shared parser would silently do the opposite.
	t.Run("zero disables the per-attempt bound", func(t *testing.T) {
		t.Setenv(key, "0")
		assert.Equal(t, time.Duration(0), parseEnvAttemptTimeoutMs(key, fallback))
	})

	t.Run("unset uses the derived bound", func(t *testing.T) {
		t.Setenv(key, "")
		assert.Equal(t, fallback, parseEnvAttemptTimeoutMs(key, fallback))
	})

	t.Run("explicit value wins", func(t *testing.T) {
		t.Setenv(key, "750")
		assert.Equal(t, 750*time.Millisecond, parseEnvAttemptTimeoutMs(key, fallback))
	})

	t.Run("negative is invalid, not a disable", func(t *testing.T) {
		t.Setenv(key, "-1")
		assert.Equal(t, fallback, parseEnvAttemptTimeoutMs(key, fallback))
	})
}
