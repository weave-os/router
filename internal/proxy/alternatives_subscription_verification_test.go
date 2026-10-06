package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"
)

type verificationAlternativeLeaser struct{ scriptedSubscriptionLeaser }

func (l *verificationAlternativeLeaser) Lease(_ context.Context, owner auth.SubscriptionOwner, _ subscriptions.Provider, _ string) (subscriptions.Lease, bool, error) {
	for _, id := range owner.ExcludedAccountIDs {
		if id == "alternative-account" {
			return subscriptions.Lease{}, true, subscriptions.ErrNoAvailableAccount
		}
	}
	return subscriptions.Lease{AccountID: "alternative-account", OwnerID: "synthetic-owner", Tier: auth.SubscriptionTierPersonal, AccessToken: "alternative-seat", ProviderAccount: "synthetic-provider"}, true, nil
}

func TestVerificationAutomaticAlternativeHTTP(t *testing.T) {
	for _, scenario := range []struct {
		name                                                          string
		explicit, excluded, max, boost, contextLimited, sessionPinned bool
		alternative                                                   bool
	}{{name: "stable", alternative: true}, {name: "boost", boost: true, alternative: true}, {name: "explicit", explicit: true}, {name: "max", max: true}, {name: "ineligible", excluded: true}, {name: "real-context-exclusion", contextLimited: true}, {name: "session-force-pin", sessionPinned: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			alternative := "gpt-6-sol"
			if scenario.contextLimited {
				alternative = "gpt-5.5-pro"
			}
			var models, bearers []string
			var sent [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				sent = append(sent, body)
				model := gjson.GetBytes(body, "model").String()
				bearer := r.Header.Get("Authorization")
				models = append(models, model)
				bearers = append(bearers, bearer)
				if bearer == "Bearer alternative-seat" && strings.Contains(model, "5.6") {
					w.WriteHeader(404)
					_, _ = io.WriteString(w, `{"error":{"code":"model_not_found","message":"synthetic account model unavailable"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"alternative answer\"}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
			}))
			defer server.Close()
			client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", server.URL)}
			client.SetCodexBaseURL(server.URL)
			resolverRequest := router.Request{HasTools: true}
			if scenario.excluded {
				resolverRequest.ExcludedModels = map[string]struct{}{alternative: {}}
			}
			if scenario.contextLimited {
				resolverRequest.EstimatedInputTokens = catalog.ContextWindowFor(alternative) + 1
			}
			resolver := policy.NewResolver(map[string]struct{}{codexCoveredModel: {}, alternative: {}}, map[string]struct{}{providers.ProviderOpenAI: {}}, func(model catalog.Model) string { return model.ID }, policy.ManagedProviderPolicy())
			resolved := resolver.Resolve(resolverRequest)
			eligible := resolved.CandidateModels()
			if scenario.excluded || scenario.contextLimited {
				require.Equal(t, []string{codexCoveredModel}, eligible)
				require.NotEmpty(t, resolved.Diagnostics)
			}
			metadata := &router.RoutingMetadata{CandidateModels: eligible, SelectionTrace: &router.SelectionTrace{SelectedGroup: "high", EffectiveOrders: map[string][]string{"high": {codexCoveredModel, alternative}}, CandidateRosterIDs: eligible}}
			var pins sessionpin.Store
			if scenario.sessionPinned {
				pins = &overwritingPinStore{pin: sessionpin.Pin{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: translate.ReasonUserForceModel, PinnedUntil: pinNeverExpires}, found: true}
			}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Metadata: metadata, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, pins, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(&verificationAlternativeLeaser{}).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
			ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
			if scenario.max {
				ctx = entitlement.WithProductScope(ctx, entitlement.PlanMax)
			}
			if scenario.boost {
				ctx = entitlement.WithProductScope(ctx, entitlement.PlanBoost)
			}
			requested := "auto"
			if scenario.explicit {
				requested = codexCoveredModel
			}
			body := `{"model":"` + requested + `","reasoning_effort":"medium","stream":true,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
			rec := httptest.NewRecorder()
			dispatchErr := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			if scenario.max {
				require.ErrorContains(t, dispatchErr, "cannot be served on max_subscription")
				require.Empty(t, bearers)
				return
			}
			require.NoError(t, dispatchErr)
			require.Contains(t, rec.Body.String(), "alternative answer")
			if scenario.alternative {
				require.Equal(t, []string{"Bearer alternative-seat", "Bearer alternative-seat"}, bearers)
				require.Contains(t, models[1], "6")
				require.True(t, servedOnSubscription(ctx))
				require.Equal(t, alternative, rec.Header().Get(HeaderRouterModel))
				require.Contains(t, rec.Body.String(), alternative)
				require.Equal(t, "read_file", gjson.GetBytes(sent[1], "tools.0.name").String())
				require.Equal(t, "medium", gjson.GetBytes(sent[1], "reasoning.effort").String())
				winner := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
				require.Equal(t, codexCoveredModel, winner.IntendedModel)
				require.Equal(t, "alternative-account", winner.SubscriptionAccountID)
				require.Equal(t, "synthetic-owner", winner.SubscriptionOwnerID)
			} else {
				require.Equal(t, []string{"Bearer alternative-seat", "Bearer synthetic-api-key"}, bearers)
				require.Equal(t, models[0], models[1])
				require.False(t, servedOnSubscription(ctx))
			}
		})
	}
}

func TestVerificationAlternativesHonorHardRequestExclusions(t *testing.T) {
	client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", "http://127.0.0.1:1")}
	svc := NewService(staticRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil)
	ctx := managedSubscriptionContext(auth.SubscriptionProviderCodex)
	selected := router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Metadata: &router.RoutingMetadata{CandidateModels: []string{codexCoveredModel, "gpt-6-sol", "gpt-6.1-sol"}}}
	request := router.Request{
		SafetyExcludedModels:          map[string]struct{}{"gpt-6-sol": {}},
		UnsignedHistoryExcludedModels: map[string]struct{}{"gpt-6.1-sol": {}},
	}
	require.Empty(t, svc.subscriptionAlternativeDecisions(ctx, request, selected))
	request = router.Request{}
	var models []string
	for _, alternative := range svc.subscriptionAlternativeDecisions(ctx, request, selected) {
		models = append(models, alternative.Model)
	}
	require.Equal(t, []string{"gpt-6-sol", "gpt-6.1-sol"}, models)
}
