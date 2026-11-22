package routingpolicy_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/routingpolicy"
)

func policyOutcome() routingpolicy.PolicyOutcome {
	return routingpolicy.PolicyOutcome{
		Route:                  &router.RoutingMetadata{RouteID: "route-1", Strategy: "sidecar", AuthoritativePerTurnSelection: true},
		Selected:               router.Decision{Provider: "anthropic", Model: "model-a"},
		ServedModel:            "model-a",
		ServedProvider:         "anthropic",
		RequestTrainingAllowed: true,
		Status:                 200,
	}
}

func TestPolicyOutcomeMatchingServeKeepsTraining(t *testing.T) {
	payload := policyOutcome().Payload()

	assert.Equal(t, true, payload["training_allowed"])
	assert.Equal(t, true, payload["selected_served_model_match"])
	assert.Empty(t, payload["error"])
	assert.NotContains(t, payload, "training_exclusion_reason")
}

func TestPolicyOutcomeAuthoritativeMismatchExcludesTraining(t *testing.T) {
	outcome := policyOutcome()
	outcome.ServedModel = "model-b"
	outcome.Effort.Mismatch = true

	payload := outcome.Payload()

	assert.False(t, outcome.TrainingAllowed())
	assert.Equal(t, false, payload["training_allowed"])
	assert.Equal(t, "selected_served_model_mismatch", payload["training_exclusion_reason"], "the model mismatch reason wins over the effort mismatch")
}

func TestPolicyOutcomeEffortMismatchExcludesTraining(t *testing.T) {
	outcome := policyOutcome()
	outcome.Effort = routingpolicy.PolicyOutcomeEffort{Arm: "high", Sent: "low", Mismatch: true}
	outcome.Err = errors.New("upstream failed")

	payload := outcome.Payload()

	assert.Equal(t, "selected_sent_effort_mismatch", payload["training_exclusion_reason"])
	assert.Equal(t, false, payload["selected_sent_effort_match"])
	assert.Equal(t, "upstream failed", payload["error"])
}
