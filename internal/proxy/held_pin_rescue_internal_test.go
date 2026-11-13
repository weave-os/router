package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
)

const (
	heldPinLunaModel   = "gpt-6-luna"
	heldPinClaudeModel = "claude-opus-5-5"
)

type heldPinGatewayClient struct {
	models                  []string
	commitBeforeLunaFailure bool
}

func (c *heldPinGatewayClient) Proxy(_ context.Context, decision router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.models = append(c.models, decision.Model)
	if decision.Model == heldPinLunaModel {
		if c.commitBeforeLunaFailure {
			_, _ = io.WriteString(w, `data: {"id":"chatcmpl-primary","object":"chat.completion.chunk","created":1,"model":"gpt-6-luna","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}`+"\n\n")
		}
		return providers.ErrUpstreamIdleTimeout
	}
	_, _ = io.WriteString(w, `data: {"id":"chatcmpl-rescue","object":"chat.completion.chunk","created":1,"model":"claude-opus-5-5","choices":[{"index":0,"delta":{"role":"assistant","content":"recovered"},"finish_reason":null}]}`+"\n\n")
	_, _ = io.WriteString(w, `data: {"id":"chatcmpl-rescue","object":"chat.completion.chunk","created":1,"model":"claude-opus-5-5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	return nil
}

func TestHeldAutomaticPinCannotRescueOutsideHeldGatewayAliases(t *testing.T) {
	svc, ctx, upstream := heldPinRescueFixture()
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		gatewayKey(providers.ProviderOpenAIGateway, "https://gateway.example.com/v1", heldPinLunaModel),
	})
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"continue"}]}`)

	err := svc.ProxyMessages(ctx, body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))

	require.ErrorIs(t, err, providers.ErrUpstreamIdleTimeout)
	assert.NotEmpty(t, upstream.models)
	for _, model := range upstream.models {
		assert.Equal(t, heldPinLunaModel, model)
	}
}

func TestHeldAutomaticPinDoesNotReplayAfterOutputCommit(t *testing.T) {
	svc, ctx, upstream := heldPinRescueFixture()
	upstream.commitBeforeLunaFailure = true
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"continue"}]}`)
	rec := httptest.NewRecorder()

	err := svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))

	require.Error(t, err)
	assert.Contains(t, rec.Body.String(), "partial")
	assert.NotContains(t, rec.Body.String(), "recovered")
	for _, model := range upstream.models {
		assert.Equal(t, heldPinLunaModel, model)
	}
}

func (*heldPinGatewayClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func heldPinRescueFixture() (*Service, context.Context, *heldPinGatewayClient) {
	strategy := router.Strategy("held-pin-rescue-test")
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Provider:    providers.ProviderOpenAIGateway,
		Model:       heldPinLunaModel,
		Reason:      "hmm_policy(classifier 'fast' (p=0.60))",
		PinnedUntil: time.Now().Add(time.Hour),
	}
	policyRouter := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAIGateway,
		Model:    heldPinClaudeModel,
		Reason:   "hmm_policy(classifier 'maximum' (p=0.18))",
		Metadata: &router.RoutingMetadata{
			ChosenScore:         0.18,
			RosterFailover:      true,
			CandidateModels:     []string{heldPinClaudeModel, heldPinLunaModel},
			RescueModels:        []string{heldPinClaudeModel},
			SelectedRosterArmID: "fresh-claude-arm",
		},
	}}
	upstream := &heldPinGatewayClient{}
	svc := NewService(nil, map[string]providers.Client{
		providers.ProviderOpenAIGateway: upstream,
	}, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: strategy,
			Router:   policyRouter,
			Capabilities: policy.Capabilities{
				SchemaVersion:                 policy.SchemaVersionV1,
				AuthoritativePerTurnSelection: true,
			},
		}).WithRetrySleep(func(context.Context, time.Duration) error { return nil })

	installationID := uuid.NewString()
	ctx := ctxWithKeys(gatewayKey(providers.ProviderOpenAIGateway, "https://gateway.example.com/v1", heldPinLunaModel, heldPinClaudeModel))
	ctx = router.WithStrategy(ctx, strategy)
	ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "key-1")
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, installationID)

	return svc, ctx, upstream
}

func TestHeldAutomaticGatewayPinRescuesToClaudeBeforeCommit(t *testing.T) {
	for _, ingress := range []struct {
		name   string
		path   string
		openAI bool
	}{
		{name: "anthropic_messages", path: "/v1/messages"},
		{name: "openai_chat", path: "/v1/chat/completions", openAI: true},
	} {
		t.Run(ingress.name, func(t *testing.T) {
			svc, ctx, upstream := heldPinRescueFixture()
			body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"continue"}],"metadata":{"user_id":"user_account__session_4dbee464-ebf7-437f-9f20-db5a6f7fe3b4"}}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, ingress.path, strings.NewReader(string(body)))
			var err error
			if ingress.openAI {
				err = svc.ProxyOpenAIChatCompletion(ctx, body, rec, req)
			} else {
				err = svc.ProxyMessages(ctx, body, rec, req)
			}

			require.NoError(t, err)
			assert.Equal(t, heldPinClaudeModel, rec.Header().Get(HeaderRouterModel))
			assert.Contains(t, rec.Body.String(), "recovered")
			require.NotEmpty(t, upstream.models)
			assert.Equal(t, heldPinClaudeModel, upstream.models[len(upstream.models)-1])
			for _, model := range upstream.models[:len(upstream.models)-1] {
				assert.Equal(t, heldPinLunaModel, model)
			}
		})
	}
}
