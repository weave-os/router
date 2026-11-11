package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

var gatewayOverflowIngresses = map[string]struct {
	body  string
	proxy func(*Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error
}{
	"anthropic_messages": {
		body: `{"model":"claude-opus-5-5","max_tokens":20000,"stream":true,"messages":[{"role":"user","content":"keep going"}]}`,
		proxy: func(s *Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error {
			return s.ProxyMessages
		},
	},
	"openai_responses": {
		body: `{"model":"claude-opus-5-5","stream":true,"input":[{"role":"user","content":"keep going"}]}`,
		proxy: func(s *Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error {
			return s.ProxyOpenAIResponses
		},
	},
	"openai_chat": {
		body: `{"model":"claude-opus-5-5","stream":true,"messages":[{"role":"user","content":"keep going"}]}`,
		proxy: func(s *Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error {
			return s.ProxyOpenAIChatCompletion
		},
	},
}

// gatewayOverflowFixture routes an ordinary turn to Claude behind the
// Anthropic gateway, with gpt-6-luna held behind the OpenAI gateway.
func gatewayOverflowFixture(anthropicErr, openAIErr error) (*Service, context.Context, *gatewayCompactionClient, *gatewayCompactionClient) {
	_, ctx, anthropicGateway, openAIGateway := gatewayCompactionFixture(anthropicErr)
	openAIGateway.failWith = openAIErr
	routed := NewService(staticRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropicGateway,
		Model:    gatewayCompactionOpusModel,
		Reason:   "test",
	}}, map[string]providers.Client{
		providers.ProviderAnthropicGateway: anthropicGateway,
		providers.ProviderOpenAIGateway:    openAIGateway,
	}, nil, false, nil, &rolePinStore{}, false, providers.ProviderAnthropic, policy.PrecompactionDefaultModel, nil).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil })
	return routed, ctx, anthropicGateway, openAIGateway
}

// Claude Code was told to compact a conversation the other gateway could
// still serve: Claude's tokenizer counts the same history well past where
// gpt-6-luna's does.
func TestGatewayOverflowOnOrdinaryTurnRescuesAcrossGateways(t *testing.T) {
	for name, ingress := range gatewayOverflowIngresses {
		t.Run(name, func(t *testing.T) {
			svc, ctx, anthropicGateway, openAIGateway := gatewayOverflowFixture(cortexPromptTooLong(), nil)
			body := []byte(ingress.body)
			rec := httptest.NewRecorder()

			err := ingress.proxy(svc)(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/x", bytes.NewReader(body)))

			require.NoError(t, err)
			assert.Equal(t, []string{gatewayCompactionOpusModel}, anthropicGateway.models, "same-gateway peers share the window that rejected it")
			assert.Equal(t, []string{catalog.ModelGPT6Luna}, openAIGateway.models)
			assert.Equal(t, catalog.ModelGPT6Luna, rec.Header().Get(HeaderRouterModel))
			assert.Contains(t, rec.Body.String(), "summary")
			assert.NotContains(t, rec.Body.String(), "prompt is too long")
		})
	}
}

// When the other gateway overflows too, the client still gets its native
// prompt-too-long, so it compacts.
func TestGatewayOverflowEverywhereOnOrdinaryTurnStillAsksToCompact(t *testing.T) {
	lunaOverflow := &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_length_exceeded"}}`),
	}
	for name, ingress := range gatewayOverflowIngresses {
		t.Run(name, func(t *testing.T) {
			svc, ctx, _, openAIGateway := gatewayOverflowFixture(cortexPromptTooLong(), lunaOverflow)
			body := []byte(ingress.body)
			rec := httptest.NewRecorder()

			err := ingress.proxy(svc)(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/x", bytes.NewReader(body)))

			require.Error(t, err)
			assert.Equal(t, []string{catalog.ModelGPT6Luna}, openAIGateway.models)
			class, ok := ClassifyDispatchError(err)
			require.True(t, ok)
			assert.Equal(t, DispatchErrorContextWindowExceeded, class.Kind)
		})
	}
}

// Only an overflow opens the cross-gateway walk; another 400 is the
// request's own fault on every gateway.
func TestGatewayNonOverflowRejectionOnOrdinaryTurnIsNotRescuedAcrossGateways(t *testing.T) {
	rejected := &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages: text content blocks must be non-empty"}}`),
	}
	for name, ingress := range gatewayOverflowIngresses {
		t.Run(name, func(t *testing.T) {
			svc, ctx, anthropicGateway, openAIGateway := gatewayOverflowFixture(rejected, nil)
			body := []byte(ingress.body)
			rec := httptest.NewRecorder()

			err := ingress.proxy(svc)(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/x", bytes.NewReader(body)))

			require.Error(t, err)
			assert.NotEmpty(t, anthropicGateway.models)
			assert.Empty(t, openAIGateway.models)
		})
	}
}

// A rescuer the estimate already puts past its own window is never tried.
func TestOverflowRescueDecisionsSkipRescuerPastItsWindow(t *testing.T) {
	svc, ctx, _, _ := gatewayOverflowFixture(nil, nil)
	failed := router.Decision{Provider: providers.ProviderAnthropicGateway, Model: gatewayCompactionOpusModel}

	fits := svc.overflowRescueDecisions(ctx, failed, "main_loop", 1000, 0, contextWindowOutputReserve)
	require.Len(t, fits, 1)
	assert.Equal(t, catalog.ModelGPT6Luna, fits[0].Model)
	assert.Equal(t, providers.ProviderOpenAIGateway, fits[0].Provider)

	lunaWindow := catalog.ContextWindowForBinding(catalog.ModelGPT6Luna, providers.ProviderOpenAIGateway)
	assert.Empty(t, svc.overflowRescueDecisions(ctx, failed, "main_loop", lunaWindow, 0, contextWindowOutputReserve))
	assert.Empty(t, svc.overflowRescueDecisions(ctx, failed, "compaction", 1000, 0, contextWindowOutputReserve), "compaction has its own rescue list")
}

// Readmitting a cooling model must not widen the rescue past the
// cross-gateway rescuers: a low-tier model is not a stand-in for Claude.
func TestOverflowRescueDecisionsKeepReadmissionInsideTheRescuers(t *testing.T) {
	const mini = "gpt-4.1-mini"
	svc, ctx, _, _ := gatewayOverflowFixture(nil, nil)
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{catalog.ModelGPT6Luna, gatewayCompactionOpusModel, mini})
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		gatewayKey(providers.ProviderAnthropicGateway, "https://gateway.example.com/anthropic", gatewayCompactionOpusModel, policy.PrecompactionDefaultModel),
		gatewayKey(providers.ProviderOpenAIGateway, "https://gateway.example.com/openai", catalog.ModelGPT6Luna, mini),
	})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{mini: time.Now().Add(time.Hour)})
	failed := router.Decision{Provider: providers.ProviderAnthropicGateway, Model: gatewayCompactionOpusModel}

	decisions := svc.overflowRescueDecisions(ctx, failed, "main_loop", 1000, 0, contextWindowOutputReserve)

	models := make([]string, 0, len(decisions))
	for _, d := range decisions {
		models = append(models, d.Model)
	}
	assert.Equal(t, []string{catalog.ModelGPT6Luna}, models)
}
