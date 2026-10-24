package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
)

// cortexPromptTooLong is the 71-byte 400 Snowflake Cortex's AI gateway
// returns for a Claude request past its 1M window.
func cortexPromptTooLong() error {
	return &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"message":"prompt is too long: 2100056 tokens \u003e 1000000 maximum"}`),
	}
}

// An overflow on the compaction summarizer used to reach Claude Code as
// prompt-too-long, which it answers by retrying the same compaction forever.
func TestGatewayCompactionContextOverflowRescuesToFittingModel(t *testing.T) {
	svc, ctx, anthropicGateway, openAIGateway := gatewayCompactionFixture(cortexPromptTooLong())

	rec, err := proxyGatewayCompaction(t, svc, ctx)

	require.NoError(t, err)
	assert.Contains(t, anthropicGateway.models, gatewayCompactionOpusModel)
	assert.Equal(t, []string{catalog.ModelGPT6Luna}, openAIGateway.models)
	assert.Equal(t, catalog.ModelGPT6Luna, rec.Header().Get(HeaderRouterModel))
	assert.Contains(t, rec.Body.String(), "summary")
	assert.NotContains(t, rec.Body.String(), "Compact the conversation")
}

// When the utility pin is itself a Claude alias, only the crossProvider-window rescuer
// can summarize the conversation.
func TestGatewayCompactionContextOverflowReachesCrossGatewayRescuer(t *testing.T) {
	svc, ctx, _, openAIGateway := gatewayCompactionFixture(cortexPromptTooLong())
	svc = svc.WithHardPinResolver(func(HardPinRequest) (string, string, bool) {
		return providers.ProviderAnthropicGateway, gatewayCompactionOpusModel, true
	})

	rec, err := proxyGatewayCompaction(t, svc, ctx)

	require.NoError(t, err)
	assert.Equal(t, []string{catalog.ModelGPT6Luna}, openAIGateway.models)
	assert.Equal(t, catalog.ModelGPT6Luna, rec.Header().Get(HeaderRouterModel))
}

// Nothing fits: the turn fails as the provider's fault, never as an
// instruction to compact.
func TestGatewayCompactionContextOverflowWithNothingFittingIsProviderFailure(t *testing.T) {
	svc, ctx, _, openAIGateway := gatewayCompactionFixture(cortexPromptTooLong())
	openAIGateway.failWith = &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","param":"input","code":"context_length_exceeded"}}`),
	}

	rec, err := proxyGatewayCompaction(t, svc, ctx)

	require.Error(t, err)
	assert.Equal(t, []string{catalog.ModelGPT6Luna}, openAIGateway.models)
	class, ok := ClassifyDispatchError(err)
	require.True(t, ok)
	assert.GreaterOrEqual(t, class.Status, http.StatusInternalServerError)
	assert.NotContains(t, class.Message, "Compact the conversation")
	assert.NotContains(t, rec.Body.String(), "Compact the conversation")
	assert.Equal(t, FailureOwnerProvider, dispatchErrorKindOwner(class.Kind))
}

// A non-compaction turn keeps the native prompt-too-long so the client compacts.
func TestNonCompactionOverflowKeepsPromptTooLong(t *testing.T) {
	assert.Equal(t, cortexPromptTooLong(), asCompactionUnservable("main_loop", router.Decision{}, cortexPromptTooLong()))
	class, ok := ClassifyDispatchError(cortexPromptTooLong())
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, class.Status)
	assert.Contains(t, class.Message, "Compact the conversation")
}

// Summarizer selection skips a summarizer whose served window the estimate
// exceeds on the binding that would serve it.
func TestCompactionSummarizersRespectBindingWindow(t *testing.T) {
	svc, ctx, _, _ := gatewayCompactionFixture(nil)
	_ = ctx
	req := router.Request{
		GatewayProviders: map[string]struct{}{providers.ProviderAnthropicGateway: {}, providers.ProviderOpenAIGateway: {}},
		CustomBindings: map[string][]string{
			gatewayCompactionOpusModel:       {providers.ProviderAnthropicGateway},
			policy.PrecompactionDefaultModel: {providers.ProviderAnthropicGateway},
			catalog.ModelGPT6Luna:            {providers.ProviderOpenAIGateway},
		},
	}
	fits := svc.compactionSummarizers(context.Background(), [sessionpin.SessionKeyLen]byte{}, "", req)
	require.NotEmpty(t, fits)

	req.EstimatedInputTokens = catalog.ContextWindowForBinding(policy.PrecompactionDefaultModel, providers.ProviderAnthropicGateway)
	for _, s := range svc.compactionSummarizers(context.Background(), [sessionpin.SessionKeyLen]byte{}, "", req) {
		assert.Greater(t, contextWindowForRequest(s.model, s.provider), req.EstimatedInputTokens, s.model)
	}
	crossProvider := svc.compactionCrossProviderRescuers(fits[0], req)
	require.Len(t, crossProvider, 1)
	assert.Equal(t, catalog.ModelGPT6Luna, crossProvider[0].model)
	assert.Equal(t, providers.ProviderOpenAIGateway, crossProvider[0].provider)
}
