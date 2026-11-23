package dispatch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
)

func TestRescueConfigEarlyStopRedoFallsBackToOffOnInvalidMode(t *testing.T) {
	ctx := context.Background()

	assert.Equal(t, flags.EarlyStopRedoOff, dispatch.RescueConfig{EarlyStopRedo: "bogus"}.EarlyStopRedoMode(ctx))
	assert.Equal(t, flags.EarlyStopRedoOff, dispatch.RescueConfig{EarlyStopRedo: flags.EarlyStopRedoOff}.EarlyStopRedoMode(ctx))
}

func TestRescueConfigSiblingFailoverFallsBackToDeploymentDefault(t *testing.T) {
	assert.False(t, dispatch.RescueConfig{}.SiblingFailoverEnabled(context.Background()))

	ctx := flags.WithOverrides(context.Background(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeySiblingFailover: true}})
	assert.True(t, dispatch.RescueConfig{}.SiblingFailoverEnabled(ctx), "org override should turn a default-off flag on")
}

func TestRescueConfigCyberRefusalFallbackModelStringOverride(t *testing.T) {
	cfg := dispatch.RescueConfig{CyberRefusalRepin: true, CyberRefusalFallbackModel: "claude-sonnet-5"}
	assert.Equal(t, "claude-sonnet-5", cfg.CyberRefusalFallbackModelFor(context.Background()))

	ctx := flags.WithOverrides(context.Background(), flags.Overrides{Strings: map[flags.Key]string{flags.KeyCyberRefusalFallback: "claude-opus-5"}})
	assert.Equal(t, "claude-opus-5", cfg.CyberRefusalFallbackModelFor(ctx))
}
