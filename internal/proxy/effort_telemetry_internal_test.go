package proxy

import (
	"testing"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/stretchr/testify/assert"
)

func TestApplyEffortTelemetry_SkippedWhenNothingResolved(t *testing.T) {
	p := turnrecord.InsertTelemetryParams{RequestID: "r"}
	applyEffortTelemetry(&p, dispatch.EffortResolution{})
	assert.Empty(t, p.EffortArm)
	assert.Empty(t, p.EffortSelected)
	assert.Empty(t, p.EffortSent)
	assert.Empty(t, p.EffortSource)
}

func TestApplyEffortTelemetry_RecordsClampedWireLevel(t *testing.T) {
	p := turnrecord.InsertTelemetryParams{}
	applyEffortTelemetry(&p, dispatch.EffortResolution{
		Arm:      "xhigh",
		Selected: "xhigh",
		Sent:     "high",
		Source:   dispatch.EffortSourceArm,
	})
	assert.Equal(t, "xhigh", p.EffortArm)
	assert.Equal(t, "xhigh", p.EffortSelected)
	assert.Equal(t, "high", p.EffortSent)
	assert.Equal(t, dispatch.EffortSourceArm, p.EffortSource)
}

func TestApplyEffortTelemetry_RecordsOverrideSource(t *testing.T) {
	p := turnrecord.InsertTelemetryParams{}
	applyEffortTelemetry(&p, dispatch.EffortResolution{
		Arm:      "low",
		Selected: "high",
		Sent:     "high",
		Source:   dispatch.EffortSourceUser,
	})
	assert.Equal(t, "low", p.EffortArm)
	assert.Equal(t, dispatch.EffortSourceUser, p.EffortSource)
}
