package proxy

import (
	"context"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

const verificationWeakToolModel = "qwen/qwen3-235b-a22b-2507"

type verificationToolResolverRouter struct {
	resolver        *policy.Resolver
	observedRequest router.Request
	resolved        policy.ResolvedCandidates
}

func (r *verificationToolResolverRouter) Route(_ context.Context, request router.Request) (router.Decision, error) {
	r.observedRequest = request
	r.resolved = r.resolver.Resolve(request)
	trace := &router.SelectionTrace{SelectedGroup: "high", EffectiveOrders: map[string][]string{"high": {codexCoveredModel, verificationWeakToolModel}}, CandidateRosterIDs: r.resolved.CandidateModels()}
	for _, diagnostic := range r.resolved.Diagnostics {
		trace.ResolverExclusions = append(trace.ResolverExclusions, router.SelectionExclusion{CatalogID: diagnostic.CatalogID, RosterID: diagnostic.RosterID, Reason: string(diagnostic.Reason)})
	}
	return router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test", Metadata: &router.RoutingMetadata{CandidateModels: r.resolved.CandidateModels(), SelectionTrace: trace}}, nil
}
func TestVerificationRealResolverWeakToolExclusionPreventsHTTP(t *testing.T) {
	resolver := policy.NewResolver(map[string]struct{}{codexCoveredModel: {}, verificationWeakToolModel: {}}, map[string]struct{}{providers.ProviderOpenAI: {}, providers.ProviderBedrock: {}}, func(model catalog.Model) string { return model.ID }, policy.ManagedProviderPolicy())
	require.Contains(t, resolver.Resolve(router.Request{}).CandidateModels(), verificationWeakToolModel, "fixture weak model must be otherwise eligible")
	var models, bearers []string
	var incompatibleRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := gjson.GetBytes(body, "model").String()
		models = append(models, model)
		bearers = append(bearers, r.Header.Get("Authorization"))
		if strings.Contains(model, "qwen") {
			incompatibleRequests++
		}
		if r.Header.Get("Authorization") == "Bearer alternative-seat" {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"code":"model_not_found","message":"synthetic account model denial"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"compatible tool answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer server.Close()
	client := &includedOnlySyntheticClient{Client: openai.NewClient("synthetic-api-key", server.URL)}
	client.SetCodexBaseURL(server.URL)
	routed := &verificationToolResolverRouter{resolver: resolver}
	// Both providers have a real HTTP transport available; rejected candidates must never reach either.
	svc := NewService(routed, map[string]providers.Client{providers.ProviderOpenAI: client, providers.ProviderBedrock: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(&verificationAlternativeLeaser{}).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}, providers.ProviderBedrock: {}})
	ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic tools"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))))
	require.True(t, routed.observedRequest.HasTools, "actual parsed tool request must drive Resolver")
	require.Contains(t, routed.resolved.Diagnostics, policy.Diagnostic{CatalogID: verificationWeakToolModel, RosterID: verificationWeakToolModel, Reason: policy.ExclusionToolCapability})
	require.NotContains(t, routed.resolved.CandidateModels(), verificationWeakToolModel)
	require.Zero(t, incompatibleRequests)
	require.Equal(t, []string{"Bearer alternative-seat", "Bearer synthetic-api-key"}, bearers)
	require.Equal(t, models[0], models[1])
	require.Contains(t, rec.Body.String(), "compatible tool answer")
}
