package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRoutingModelDoesNotReadLegacyBetaPreference(t *testing.T) {
	svc := (&Service{}).WithSessionStrategyStore(&betaTestPreferenceStore{getErr: errors.New("legacy preference must not be read")})
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	key := [sessionpin.SessionKeyLen]byte{1}
	installation := uuid.New()
	_, err := svc.applySessionStrategy(ctx, installation, key)
	require.Error(t, err)
	ctx = router.WithRoutingModel(ctx, router.ModelAutomatic)
	selected, err := svc.applySessionStrategy(ctx, installation, key)
	require.NoError(t, err)
	require.Equal(t, router.StrategyHMMEmbedding, router.StrategyFromContext(selected))
}

func TestRoutingModelRedirectsLegacyBetaCommandsWithoutChangingPreferences(t *testing.T) {
	for _, model := range []router.RoutingModel{router.ModelAutomatic, router.ModelHMM, router.ModelQwen} {
		for _, commandText := range []string{"/beta", "/beta-llm"} {
			t.Run(string(model)+commandText, func(t *testing.T) {
				store := &betaTestPreferenceStore{}
				svc := (&Service{}).WithSessionStrategyStore(store)
				ctx := router.WithRoutingModel(context.Background(), model)
				envelope := betaTestEnvelope(t, commandText, true)
				command, found := envelope.ExtractBetaCommand()
				require.True(t, found)
				response := httptest.NewRecorder()
				key := [sessionpin.SessionKeyLen]byte{1}
				require.NoError(t, svc.handleBetaCommand(ctx, response, envelope, command, uuid.New(), key, key, 1))
				require.Contains(t, response.Body.String(), "wv router set-model")
				require.Zero(t, store.toggles)
				require.False(t, store.found)
			})
		}
	}
}

type routingModelCaptureProvider struct{ betaCaptureProvider }

func (p *routingModelCaptureProvider) Proxy(ctx context.Context, _ router.Decision, prepared providers.PreparedRequest, w http.ResponseWriter, request *http.Request) error {
	return p.Passthrough(ctx, prepared, w, request)
}

func (p *routingModelCaptureProvider) Passthrough(_ context.Context, prepared providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	p.body = append([]byte(nil), prepared.Body...)
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(`{"id":"synthetic-reply","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"selected policy reply"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	return err
}

func TestRoutingModelDispatchesConcreteModelAcrossClientProtocols(t *testing.T) {
	for _, choice := range []struct {
		model    router.RoutingModel
		strategy router.Strategy
	}{
		{model: router.ModelAutomatic, strategy: router.StrategyHMMEmbedding},
		{model: router.ModelHMM, strategy: router.StrategyHMMBeta},
		{model: router.ModelQwen, strategy: router.StrategyHMMQwen},
	} {
		for _, tc := range []struct {
			path string
			body string
			call func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
		}{
			{path: "/v1/messages", body: `{"model":"` + string(choice.model) + `","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`, call: (*Service).ProxyMessages},
			{path: "/v1/responses", body: `{"model":"` + string(choice.model) + `","input":[{"role":"user","content":"hello"}]}`, call: (*Service).ProxyOpenAIResponses},
		} {
			t.Run(string(choice.model)+tc.path, func(t *testing.T) {
				const completionModel = "claude-haiku-4-5"
				defaultRouter := &betaTestRouter{}
				hmmRouter := &betaTestRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: completionModel}}
				provider := &routingModelCaptureProvider{}
				svc := NewService(defaultRouter, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, completionModel, nil).
					WithPolicyStrategy(policy.StrategySpec{Strategy: choice.strategy, Router: hmmRouter})
				ctx := router.WithRoutingModel(router.WithStrategy(context.Background(), choice.strategy), choice.model)
				request := httptest.NewRequest(http.MethodPost, tc.path, nil)
				response := httptest.NewRecorder()
				require.NoError(t, tc.call(svc, ctx, []byte(tc.body), response, request))
				require.Zero(t, defaultRouter.calls)
				require.Equal(t, 1, hmmRouter.calls)
				require.Equal(t, completionModel, gjson.GetBytes(provider.body, "model").String())
				require.Contains(t, response.Body.String(), "selected policy reply")
				require.False(t, strings.Contains(string(provider.body), string(choice.model)))
			})
		}
	}
}
