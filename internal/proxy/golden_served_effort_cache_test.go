package proxy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func TestServedTurnWithEffortRecordsTheCacheUnderTheBareModel(t *testing.T) {
	served := decision(providers.ProviderAnthropic, goldenAnthropicModel)
	served.Effort = "high"
	sc := goldenScenario{
		name: "served_effort_records_bare_model_cache", ingress: ingressOpenAI,
		decisions: []router.Decision{served},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "plan")}},
	}
	h := newGoldenHarness(t, sc)
	h.serve(t, sc.ingress, goldenRequest{body: conversationOpenAIBody(conversationOpenAIUser(goldenConversationOpening))})

	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	require.Len(t, h.pins.pins, 1)
	for _, pin := range h.pins.pins {
		assert.Contains(t, pin.ModelCaches, goldenAnthropicModel)
		assert.NotContains(t, pin.ModelCaches, goldenAnthropicModel+":high")
	}
}
