package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestApplyEffortTelemetry_SkippedWhenNothingResolved(t *testing.T) {
	p := InsertTelemetryParams{RequestID: "r"}
	applyEffortTelemetry(&p, effortResolution{})
	assert.Empty(t, p.EffortArm)
	assert.Empty(t, p.EffortSelected)
	assert.Empty(t, p.EffortSent)
	assert.Empty(t, p.EffortSource)
}

func TestApplyEffortTelemetry_RecordsClampedWireLevel(t *testing.T) {
	p := InsertTelemetryParams{}
	applyEffortTelemetry(&p, effortResolution{
		Arm:      "xhigh",
		Selected: "xhigh",
		Sent:     "high",
		Source:   effortSourceArm,
	})
	assert.Equal(t, "xhigh", p.EffortArm)
	assert.Equal(t, "xhigh", p.EffortSelected)
	assert.Equal(t, "high", p.EffortSent)
	assert.Equal(t, effortSourceArm, p.EffortSource)
}

func TestApplyEffortTelemetry_RecordsOverrideSource(t *testing.T) {
	p := InsertTelemetryParams{}
	applyEffortTelemetry(&p, effortResolution{
		Arm:      "low",
		Selected: "high",
		Sent:     "high",
		Source:   effortSourceUser,
	})
	assert.Equal(t, "low", p.EffortArm)
	assert.Equal(t, effortSourceUser, p.EffortSource)
}
