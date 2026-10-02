package proxy_test

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const codexLunaCoverageModel = "gpt-6-luna"

func TestBoostCodexLunaSubscriptionCoverage(t *testing.T) {
	for _, reason := range []billing.SubscriptionOnlyReason{
		billing.SubscriptionOnlyLinkedFirst,
		billing.SubscriptionOnlyCreditsDepleted,
	} {
		t.Run(string(reason), func(t *testing.T) {
			selection := &fakeRouter{decision: router.Decision{
				Provider: providers.ProviderOpenAI,
				Model:    codexLunaCoverageModel,
			}}
			upstream := &fakeProvider{proxyResponse: codexResponsesStream}
			service := proxy.NewService(selection, map[string]providers.Client{
				providers.ProviderOpenAI: upstream,
			}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexLunaCoverageModel, nil).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})

			body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Refactor the auth middleware and add tests."}],"max_tokens":4096,"stream":true,"tools":[{"type":"function","function":{"name":"edit_file","parameters":{"type":"object"}}}]}`
			recorder, request := codexSubRequest(t, body)
			ctx := entitlement.WithProductScope(context.Background(), entitlement.PlanBoost)
			ctx = billing.WithSubscriptionOnly(ctx, reason)

			require.NoError(t, service.ProxyOpenAIChatCompletion(ctx, []byte(body), recorder, request))
			require.NotNil(t, selection.capturedReq, "the request must reach automatic selection")
			assert.NotContains(t, selection.capturedReq.ExcludedModels, codexLunaCoverageModel)
			if reason == billing.SubscriptionOnlyCreditsDepleted {
				assert.Contains(t, selection.capturedReq.ExcludedModels, "gpt-5.4-nano")
			} else {
				assert.NotContains(t, selection.capturedReq.ExcludedModels, "gpt-5.4-nano")
			}
			require.Len(t, upstream.proxyCreds, 1)
			require.NotNil(t, upstream.proxyCreds[0])
			assert.True(t, upstream.proxyCreds[0].OAuth)
			assert.Equal(t, requestcontext.SourceCodexSubscription, upstream.proxyCreds[0].Source)
			assert.Equal(t, []byte(codexSubToken), upstream.proxyCreds[0].APIKey)
			assert.Equal(t, []byte(codexSubAccountID), upstream.proxyCreds[0].AccountID)
			assert.Equal(t, providers.EndpointResponses, upstream.proxyEndpoints[0])
			assert.Equal(t, codexLunaCoverageModel, gjson.GetBytes(upstream.proxyBodies[0], "model").String())
			assert.Equal(t, codexLunaCoverageModel, recorder.Header().Get(proxy.HeaderRouterModel))
			assert.Equal(t, http.StatusOK, recorder.Code)
		})
	}
}
