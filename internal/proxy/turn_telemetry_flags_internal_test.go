package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/flags"

	"github.com/stretchr/testify/assert"
)

func TestTelemetryFlagsFallBackToDeploymentDefault(t *testing.T) {
	ctx := context.Background()
	assert.True(t, TelemetryFlags{TurnSignalCapture: true}.resolveTurnSignalCapture(ctx))
	assert.False(t, TelemetryFlags{}.resolveNativeAnthropicResponseSignals(ctx))
	assert.True(t, TelemetryFlags{NativeOpenAIResponseSignals: true}.resolveNativeOpenAIResponseSignals(ctx))
}

func TestTelemetryFlagsPerOrgOverrideBeatsDeploymentDefault(t *testing.T) {
	ctx := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{
			flags.KeyTurnSignalCapture:              false,
			flags.KeyNativeAnthropicResponseSignals: true,
			flags.KeyNativeOpenAIResponseSignals:    false,
		},
	})
	defaults := TelemetryFlags{TurnSignalCapture: true, NativeOpenAIResponseSignals: true}
	assert.False(t, defaults.resolveTurnSignalCapture(ctx), "org override should turn a default-on flag off")
	assert.True(t, defaults.resolveNativeAnthropicResponseSignals(ctx), "org override should turn a default-off flag on")
	assert.False(t, defaults.resolveNativeOpenAIResponseSignals(ctx))
}

func TestNewServiceDefaultsTelemetryFlagsOn(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, false, "", "", nil)
	assert.Equal(t, TelemetryFlags{TurnSignalCapture: true, NativeAnthropicResponseSignals: true, NativeOpenAIResponseSignals: true}, svc.turnTelemetry.flags)
}
