package proxy

import (
	"testing"

	"weave-os/router/internal/ingress"

	"github.com/stretchr/testify/assert"
)

func TestWithDepsAppliesTelemetryFlags(t *testing.T) {
	want := TelemetryFlags{NativeAnthropicResponseSignals: true}
	svc := WithDeps(NewService(nil, nil, nil, nil, false, "", "", nil), Deps{Telemetry: TelemetryDeps{Flags: &want}})
	assert.Equal(t, want, svc.turnTelemetry.flags)
}

func TestWithDepsKeepsTelemetryFlagDefaultsWhenUnset(t *testing.T) {
	svc := WithDeps(NewService(nil, nil, nil, nil, false, "", "", nil), Deps{})
	assert.Equal(t, TelemetryFlags{TurnSignalCapture: true, NativeAnthropicResponseSignals: true, NativeOpenAIResponseSignals: true}, svc.turnTelemetry.flags)
}

func TestWithDepsAppliesIngressFormats(t *testing.T) {
	formats := ingress.New(0)
	formats.CCAutonomySystemAppend = true
	formats.SearchRequirementDecayTurns = 7
	svc := WithDeps(NewService(nil, nil, nil, nil, false, "", "", nil), Deps{Ingress: &formats})
	assert.True(t, svc.ingress.CCAutonomySystemAppend)
	assert.Equal(t, 7, svc.ingress.SearchRequirementDecayTurns)
}

func TestWithDepsKeepsIngressDefaultsWhenUnset(t *testing.T) {
	svc := WithDeps(NewService(nil, nil, nil, nil, false, "", "", nil), Deps{})
	assert.Equal(t, ingress.New(0), svc.ingress)
}
