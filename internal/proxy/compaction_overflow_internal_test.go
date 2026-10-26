package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
)

// cortexPromptTooLong is the 71-byte 400 Cortex's AI gateway
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

// A conversation the estimate already puts past every Claude summarizer's
// window drops them all, leaving the Claude utility pin; its overflow must
// still reach a larger-window model on the other gateway.
func TestGatewayCompactionPastEverySummarizerWindowReachesCrossGatewayRescuer(t *testing.T) {
	const sol = "gpt-6-sol"
	svc, ctx, anthropicGateway, openAIGateway := gatewayCompactionFixture(cortexPromptTooLong())
	svc = svc.WithHardPinResolver(func(HardPinRequest) (string, string, bool) {
		return providers.ProviderAnthropicGateway, gatewayCompactionOpusModel, true
	})
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{sol, gatewayCompactionOpusModel})
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		gatewayKey(providers.ProviderAnthropicGateway, "https://gateway.example.com/anthropic", gatewayCompactionOpusModel, policy.PrecompactionDefaultModel),
		gatewayKey(providers.ProviderOpenAIGateway, "https://gateway.example.com/openai", sol),
	})
	claudeWindow := catalog.ContextWindowForBinding(gatewayCompactionOpusModel, providers.ProviderAnthropicGateway)
	require.Greater(t, catalog.ContextWindowForBinding(sol, providers.ProviderOpenAIGateway), claudeWindow+2*contextWindowOutputReserve)
	history := strings.Repeat("a", 4*(claudeWindow+contextWindowOutputReserve))
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":20000,"stream":true,"system":"Your task is to create a detailed summary of the conversation so far.","messages":[{"role":"user","content":"` + history + `"}]}`)
	rec := httptest.NewRecorder()

	err := svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)))

	require.NoError(t, err)
	assert.Equal(t, []string{gatewayCompactionOpusModel}, anthropicGateway.models, "only the utility pin is tried on the Claude gateway")
	assert.Equal(t, []string{sol}, openAIGateway.models)
	assert.Equal(t, sol, rec.Header().Get(HeaderRouterModel))
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
	summarizers := svc.compactionSummarizers(context.Background(), [sessionpin.SessionKeyLen]byte{}, "", req)
	require.NotEmpty(t, summarizers)

	req.EstimatedInputTokens = catalog.ContextWindowForBinding(policy.PrecompactionDefaultModel, providers.ProviderAnthropicGateway)
	for _, s := range svc.compactionSummarizers(context.Background(), [sessionpin.SessionKeyLen]byte{}, "", req) {
		assert.Greater(t, contextWindowForRequest(s.model, s.provider), req.EstimatedInputTokens, s.model)
	}
	crossProvider := svc.crossGatewayRescuers(ctx, summarizers[0], req)
	require.Len(t, crossProvider, 1)
	assert.Equal(t, catalog.ModelGPT6Luna, crossProvider[0].model)
	assert.Equal(t, providers.ProviderOpenAIGateway, crossProvider[0].provider)
}

// A streaming compaction whose routing notices already reached the client
// renders the final error in-stream, so it must be converted before that
// write, not after.
func TestStreamingCompactionOverflowingEverywhereNeverAsksToCompact(t *testing.T) {
	const codexCompaction = "You are performing a CONTEXT CHECKPOINT COMPACTION. Summarize the conversation."
	ingresses := map[string]struct {
		body  string
		proxy func(*Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		"anthropic_messages": {
			body: `{"model":"claude-opus-5-5","max_tokens":20000,"stream":true,"system":"Your task is to create a detailed summary of the conversation so far.","messages":[{"role":"user","content":"summarize"}]}`,
			proxy: func(s *Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error {
				return s.ProxyMessages
			},
		},
		"openai_chat": {
			body: `{"model":"gpt-6-luna","stream":true,"messages":[{"role":"user","content":"` + codexCompaction + `"}]}`,
			proxy: func(s *Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error {
				return s.ProxyOpenAIChatCompletion
			},
		},
		"openai_responses": {
			body: `{"model":"gpt-6-luna","stream":true,"input":[{"role":"user","content":"` + codexCompaction + `"}]}`,
			proxy: func(s *Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error {
				return s.ProxyOpenAIResponses
			},
		},
	}
	for name, ingress := range ingresses {
		t.Run(name, func(t *testing.T) {
			svc, ctx, anthropicGateway, openAIGateway := gatewayCompactionFixture(cortexPromptTooLong())
			openAIGateway.failWith = &providers.UpstreamErrorResponse{
				Status: http.StatusBadRequest,
				Body:   []byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","param":"input","code":"context_length_exceeded"}}`),
			}
			body := []byte(ingress.body)
			rec := httptest.NewRecorder()

			err := ingress.proxy(svc)(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/x", bytes.NewReader(body)))

			require.Error(t, err)
			require.NotEmpty(t, append(anthropicGateway.models, openAIGateway.models...))
			assert.ErrorAs(t, err, new(*CompactionUnservableError))
			assert.NotContains(t, rec.Body.String(), "Compact the conversation")
			assert.NotContains(t, rec.Body.String(), "context_length_exceeded")
			assert.NotContains(t, rec.Body.String(), "prompt is too long")
		})
	}
}

// Only Cortex gateways were measured; another tenant's gateway of
// the same provider type keeps the catalog window.
func TestGatewayContextWindowAppliesMeasuredWindowOnlyToCortex(t *testing.T) {
	windowOn := func(baseURL string) int {
		ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			gatewayKey(providers.ProviderOpenAIGateway, baseURL, catalog.ModelGPT6Luna),
		})
		return gatewayContextWindow(cortexGatewayProvidersForRequest(ctx), catalog.ModelGPT6Luna, providers.ProviderOpenAIGateway)
	}

	assert.Equal(t, 910_000, windowOn("https://acme-xy12345.snowflakecomputing.com/api/v2/aigateways/acme"))
	assert.Equal(t, catalog.ContextWindowFor(catalog.ModelGPT6Luna), windowOn("https://tenant.openai.azure.com/openai"))
	assert.Equal(t, catalog.ContextWindowFor(catalog.ModelGPT6Luna), windowOn("https://snowflakecomputing.com.evil.example/v1"))
}
