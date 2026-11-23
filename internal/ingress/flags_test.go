package ingress_test

import (
	"context"
	"testing"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/ingress"

	"github.com/stretchr/testify/assert"
)

func TestCCTaskToolsCrossVendorResolution(t *testing.T) {
	formats := ingress.New(0)
	assert.False(t, formats.TaskToolsCrossVendor(context.Background()),
		"the task-list tools stay stripped until an installation opts in")

	on := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeyCCTaskToolsCrossVendor: true},
	})
	assert.True(t, formats.TaskToolsCrossVendor(on))

	deploymentOn := ingress.New(0)
	deploymentOn.CCTaskToolsCrossVendor = true
	assert.True(t, deploymentOn.TaskToolsCrossVendor(context.Background()))
	off := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeyCCTaskToolsCrossVendor: false},
	})
	assert.False(t, deploymentOn.TaskToolsCrossVendor(off))
}

func TestNewKeepsServiceDefaults(t *testing.T) {
	formats := ingress.New(0)
	assert.True(t, formats.OpenAIResponsesBroad)
	assert.True(t, formats.CCOrchToolsCrossVendor)
	assert.Equal(t, 3, formats.SearchRequirementDecayTurns)
	assert.False(t, formats.ScopedSearchRequirement)
	assert.False(t, formats.EscapeNormalize)
	assert.False(t, formats.CCTaskToolsCrossVendor)
	assert.False(t, formats.CCAutonomySystemAppend)
	assert.False(t, formats.CCWorkspaceSystemAppend)
	assert.Nil(t, formats.WebSearch)
}
