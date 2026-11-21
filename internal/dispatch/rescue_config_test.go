package dispatch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
)

func TestRescueConfigReturnsConfiguredDefaultsWithoutOverrides(t *testing.T) {
	cfg := dispatch.RescueConfig{SiblingFailover: true, CyberRefusalRetry: true, CyberRefusalFallbackModel: "fallback-model"}
	ctx := context.Background()

	assert.True(t, cfg.SiblingFailoverEnabled(ctx))
	assert.False(t, cfg.CyberRefusalRepinEnabled(ctx))
	assert.True(t, cfg.CyberRefusalRetryEnabled(ctx))
	assert.False(t, cfg.AnthropicServerSideFallbackEnabled(ctx))
	assert.Equal(t, "fallback-model", cfg.CyberRefusalFallback(ctx))
}

func TestRescueConfigEarlyStopRedoFallsBackToOffOnInvalidMode(t *testing.T) {
	ctx := context.Background()

	assert.Equal(t, flags.EarlyStopRedoOff, dispatch.RescueConfig{EarlyStopRedo: "bogus"}.EarlyStopRedoMode(ctx))
	assert.Equal(t, flags.EarlyStopRedoOff, dispatch.RescueConfig{EarlyStopRedo: flags.EarlyStopRedoOff}.EarlyStopRedoMode(ctx))
}
