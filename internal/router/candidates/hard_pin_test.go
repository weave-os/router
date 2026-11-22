package candidates_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/turntype"
)

func TestHardPinDecision(t *testing.T) {
	assert.Equal(t, router.Decision{Provider: "anthropic", Model: "claude-haiku-4-5", Reason: "probe_hard_pin"},
		candidates.HardPinDecision(turntype.Probe, "anthropic", "claude-haiku-4-5"))
	assert.Equal(t, "sub_agent_dispatch_hard_pin", candidates.HardPinReason(turntype.SubAgentDispatch))
}
