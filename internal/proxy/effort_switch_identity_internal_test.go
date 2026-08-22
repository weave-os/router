package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
)

// An effort change on the SAME model rewrites the Anthropic prompt-cache prefix
// and invalidates thinking-block signatures exactly like a model change does,
// so it has to register as a switch. Before serving identities were compared,
// both turns produced Decision.Model == "claude-opus-5" and the switch went
// undetected — carrying stale signed thinking blocks into a 400.
func TestModelSwitched_EffortChangeOnSameModelCountsAsSwitch(t *testing.T) {
	res := turnLoopResult{
		PriorServedModel: "claude-opus-5:low",
		Decision:         router.Decision{Model: "claude-opus-5", Effort: "xhigh"},
	}

	assert.True(t, res.modelSwitched(),
		"low -> xhigh on the same model must count as a switch")
}

func TestModelSwitched_SameModelAndEffortIsNotASwitch(t *testing.T) {
	res := turnLoopResult{
		PriorServedModel: "claude-opus-5:xhigh",
		Decision:         router.Decision{Model: "claude-opus-5", Effort: "xhigh"},
	}

	assert.False(t, res.modelSwitched(),
		"an unchanged model+effort must not report a switch")
}

// Pins written before effort existed hold a bare model ID. Comparing that to a
// new "model:effort" identity reports a switch, which is the conservative
// direction (strip stale blocks, lose one cache prefix) rather than the unsafe
// one.
func TestModelSwitched_LegacyBarePinReportsSwitchAgainstEffortIdentity(t *testing.T) {
	res := turnLoopResult{
		PriorServedModel: "claude-opus-5",
		Decision:         router.Decision{Model: "claude-opus-5", Effort: "high"},
	}

	assert.True(t, res.modelSwitched(),
		"a legacy bare pin must fail safe toward stripping")
}

func TestModelSwitched_NoEffortEitherSideBehavesAsBefore(t *testing.T) {
	same := turnLoopResult{
		PriorServedModel: "claude-opus-5",
		Decision:         router.Decision{Model: "claude-opus-5"},
	}
	changed := turnLoopResult{
		PriorServedModel: "claude-opus-5",
		Decision:         router.Decision{Model: "gpt-5.6-sol"},
	}

	assert.False(t, same.modelSwitched(), "effort-free no-op must not switch")
	assert.True(t, changed.modelSwitched(), "effort-free model change must switch")
}

func TestServedIdentity_FoldsEffortAndOmitsWhenAbsent(t *testing.T) {
	assert.Equal(t, "claude-opus-5:xhigh",
		router.Decision{Model: "claude-opus-5", Effort: "xhigh"}.ServedIdentity())
	assert.Equal(t, "claude-opus-5",
		router.Decision{Model: "claude-opus-5"}.ServedIdentity())
}

// ExcludedModels / SafetyExcludedModels are keyed on bare catalog IDs, so the
// loop-breaker must strip effort off the pin's serving identity. Leaving it on
// would make the exclusion silently never match, disabling loop-breaking for
// every effort-carrying turn.
func TestMaxedOutServedModel_StripsEffortSoExclusionMatches(t *testing.T) {
	pin := sessionpin.Pin{
		LastServedModel:  "claude-opus-5:xhigh",
		LastOutputTokens: prevTurnMaxedOutThreshold,
	}

	assert.Equal(t, "claude-opus-5", maxedOutServedModel(pin),
		"exclusion keys are bare catalog IDs")
}

func TestMaxedOutServedModel_BareIdentityUnchanged(t *testing.T) {
	pin := sessionpin.Pin{
		LastServedModel:  "claude-opus-5",
		LastOutputTokens: prevTurnMaxedOutThreshold,
	}

	assert.Equal(t, "claude-opus-5", maxedOutServedModel(pin))
}

func TestBaseModelOf(t *testing.T) {
	assert.Equal(t, "claude-opus-5", baseModelOf("claude-opus-5:xhigh"))
	assert.Equal(t, "claude-opus-5", baseModelOf("claude-opus-5"))
	assert.Equal(t, "", baseModelOf(""))
}
