package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type routingModelCaptureProvider struct{ captureTestProvider }

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
				defaultRouter := &captureTestRouter{}
				hmmRouter := &captureTestRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: completionModel}}
				provider := &routingModelCaptureProvider{}
				svc := withHMMPorts(NewService(defaultRouter, dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: provider}), nil, nil, false, providers.ProviderAnthropic, completionModel, nil)).
					WithPolicyStrategy(policy.StrategySpec{Strategy: choice.strategy, Router: hmmRouter})
				ctx := router.WithStrategySelection(context.Background(), router.StrategySelection{Strategy: choice.strategy, RoutingModel: choice.model})
				request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tc.path, nil)
				response := httptest.NewRecorder()
				require.NoError(t, tc.call(svc, ctx, []byte(tc.body), response, request))
				require.Zero(t, defaultRouter.calls)
				require.Equal(t, 1, hmmRouter.calls)
				require.Equal(t, completionModel, gjson.GetBytes(provider.body, "model").String())
				require.Contains(t, response.Body.String(), "selected policy reply")
				require.NotContains(t, string(provider.body), string(choice.model))
			})
		}
	}
}
