package proxy_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBoostLinkedFirstHonorsForcedModel(t *testing.T) {
	const astraModel = "gpt-6-astra"
	const opusModel = "claude-opus-5"
	for _, tc := range []struct {
		name      string
		model     string
		provider  string
		stored    bool
		responses bool
		messages  bool
	}{
		{name: "Astra header", model: astraModel, provider: providers.ProviderOpenAI},
		{name: "Astra stored pin", model: astraModel, provider: providers.ProviderOpenAI, stored: true},
		{name: "Astra Responses", model: astraModel, provider: providers.ProviderOpenAI, responses: true},
		{name: "Astra Messages", model: astraModel, provider: providers.ProviderOpenAI, messages: true},
		{name: "Opus outside linked provider", model: opusModel, provider: providers.ProviderAnthropic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-6.1-sol"}}
			upstream := &fakeProvider{proxyResponse: codexResponsesStream}
			if tc.provider == providers.ProviderAnthropic {
				upstream.proxyResponse = bypassStreamResponse
			}
			store := newFakePinStore()
			if tc.stored {
				store.hasPin = true
				store.pin = sessionpin.Pin{
					Model: tc.model, Provider: tc.provider, Effort: "high",
					Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(time.Hour),
				}
			}
			service := proxy.NewService(selection, map[string]providers.Client{
				tc.provider: upstream,
			}, nil, false, nil, store, false, providers.ProviderOpenAI, "gpt-6.1-sol", nil).
				WithDeploymentKeyedProviders(map[string]struct{}{tc.provider: {}})
			body := `{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"Review this change."}],"max_tokens":4096,"stream":true}`
			if tc.responses {
				body = `{"model":"gpt-6.1-sol","input":[{"role":"user","content":"Review this change."}],"stream":true}`
			} else if tc.messages {
				body = `{"model":"claude-opus-5","messages":[{"role":"user","content":"Review this change."}],"max_tokens":4096,"stream":true}`
			}
			recorder, request := codexSubRequest(t, body)
			if !tc.stored {
				request.Header.Set(proxy.ForceModelHeader, tc.model+":high")
			}
			ctx := entitlement.WithProductScope(context.Background(), entitlement.PlanBoost)
			ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
			var err error
			switch {
			case tc.responses:
				err = service.ProxyOpenAIResponses(ctx, []byte(body), recorder, request)
			case tc.messages:
				err = service.ProxyMessages(ctx, []byte(body), recorder, request)
			default:
				err = service.ProxyOpenAIChatCompletion(ctx, []byte(body), recorder, request)
			}
			require.NoError(t, err)
			assert.Zero(t, selection.routeCalls, "funding preference must not replace a forced model")
			assert.Equal(t, tc.model, recorder.Header().Get(proxy.HeaderRouterModel))
			require.Len(t, upstream.proxyBodies, 1)
			assert.Equal(t, tc.model, gjson.GetBytes(upstream.proxyBodies[0], "model").String())
			if tc.provider == providers.ProviderOpenAI {
				assert.Equal(t, "high", gjson.GetBytes(upstream.proxyBodies[0], "reasoning.effort").String())
			}
			if upstream.proxyCreds[0] != nil {
				assert.False(t, upstream.proxyCreds[0].OAuth, "uncovered models must use infrastructure credentials")
			}
			assert.NotContains(t, recorder.Body.String(), "could not be served")
			assert.Equal(t, http.StatusOK, recorder.Code)
		})
	}
}
