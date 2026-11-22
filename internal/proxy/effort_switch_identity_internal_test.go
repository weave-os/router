package proxy

import (
	"testing"
	"weave-os/router/internal/routingpolicy"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
)

// An effort change on the same model invalidates thinking-block signatures
// and the prompt-cache prefix exactly like a model change, so it must count as a switch.
func TestModelSwitched_EffortChangeOnSameModelCountsAsSwitch(t *testing.T) {
	res := routingpolicy.TurnRoute{
		PriorServedModel: "claude-opus-5:low",
		Decision:         router.Decision{Model: "claude-opus-5", Effort: "xhigh"},
	}

	assert.True(t, res.ModelSwitched(),
		"low -> xhigh on the same model must count as a switch")
}

func TestModelSwitched_SameModelAndEffortIsNotASwitch(t *testing.T) {
	res := routingpolicy.TurnRoute{
		PriorServedModel: "claude-opus-5:xhigh",
		Decision:         router.Decision{Model: "claude-opus-5", Effort: "xhigh"},
	}

	assert.False(t, res.ModelSwitched(),
		"an unchanged model+effort must not report a switch")
}

// A bare legacy pin compared against a "model:effort" identity reports a switch — the
// conservative direction — rather than the unsafe one.
func TestModelSwitched_LegacyBarePinReportsSwitchAgainstEffortIdentity(t *testing.T) {
	res := routingpolicy.TurnRoute{
		PriorServedModel: "claude-opus-5",
		Decision:         router.Decision{Model: "claude-opus-5", Effort: "high"},
	}

	assert.True(t, res.ModelSwitched(),
		"a legacy bare pin must fail safe toward stripping")
}

func TestModelSwitched_NoEffortEitherSideBehavesAsBefore(t *testing.T) {
	same := routingpolicy.TurnRoute{
		PriorServedModel: "claude-opus-5",
		Decision:         router.Decision{Model: "claude-opus-5"},
	}
	changed := routingpolicy.TurnRoute{
		PriorServedModel: "claude-opus-5",
		Decision:         router.Decision{Model: "gpt-5.6-sol"},
	}

	assert.False(t, same.ModelSwitched(), "effort-free no-op must not switch")
	assert.True(t, changed.ModelSwitched(), "effort-free model change must switch")
}

func TestServedIdentity_FoldsEffortAndOmitsWhenAbsent(t *testing.T) {
	assert.Equal(t, "claude-opus-5:xhigh",
		router.Decision{Model: "claude-opus-5", Effort: "xhigh"}.ServedIdentity())
	assert.Equal(t, "claude-opus-5",
		router.Decision{Model: "claude-opus-5"}.ServedIdentity())
}
