package proxy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func TestTranslatedChatTurnRecordsTheDispatchedCacheWriteTTL(t *testing.T) {
	sc := goldenScenario{
		name: "translated_chat_records_cache_write_ttl", ingress: ingressOpenAI,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "plan")}},
	}
	h := newGoldenHarness(t, sc)
	h.serve(t, sc.ingress, goldenRequest{body: conversationOpenAIBody(conversationOpenAIUser(goldenConversationOpening))})

	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	require.Len(t, h.pins.pins, 1)
	for _, pin := range h.pins.pins {
		require.Contains(t, pin.ModelCaches, goldenAnthropicModel)
		assert.Equal(t, 300, pin.ModelCaches[goldenAnthropicModel].WriteTTLSeconds)
	}
}
