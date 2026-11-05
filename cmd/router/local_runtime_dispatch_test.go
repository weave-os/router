package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/server"
)

func TestLocalCompositionRejectsUnsupportedProfileBeforeLoadingRegistry(t *testing.T) {
	t.Setenv("ROUTER_LOCAL_PROFILE_KEY", "unsupported-product-profile")
	t.Setenv("ROUTER_LOCAL_POLICY_TARGET", "")
	loaded, closeRegistry, err := buildLocalServingRuntime(context.Background(), server.DeploymentModeSelfHosted, nil)
	require.ErrorContains(t, err, "requires default profile")
	require.Nil(t, loaded)
	require.Nil(t, closeRegistry)
}

type localFakeProvider struct {
	models []string
}

func (p *localFakeProvider) Proxy(_ context.Context, decision router.Decision, prepared providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	p.models = append(p.models, decision.Model)
	if len(p.models) == 1 {
		return &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}
	}
	_, _ = w.Write([]byte(`{"result":"authored offline response"}`))
	return nil
}
func (p *localFakeProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func TestLocalPinnedCurrentHMMDecisionUsesUnchangedExecutorRetryPath(t *testing.T) {
	identity := policyregistry.ClassifierIdentity{ArtifactID: "synthetic-classifier", PackageSHA256: strings.Repeat("a", 64),
		ImageDigest: "sha256:" + strings.Repeat("b", 64), WireSchema: policy.SchemaVersionV4,
		ClassOrder: []string{"high"}, TaxonomySHA256: strings.Repeat("c", 64)}
	classifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/readyz":
			require.NoError(t, json.NewEncoder(w).Encode(policyclient.ClassifierHealth{SchemaVersion: identity.WireSchema,
				ClassifierArtifactID: identity.ArtifactID, ClassifierSHA256: identity.PackageSHA256,
				ClassifierImageDigest: identity.ImageDigest, ClassifierClassOrder: identity.ClassOrder, ClassifierTaxonomySHA: identity.TaxonomySHA256}))
		case "/capabilities":
			require.NoError(t, json.NewEncoder(w).Encode(policy.Capabilities{SchemaVersion: policy.SchemaVersionV4}))
		case "/route":
			var query struct {
				RouteID string `json:"route_id"`
			}
			require.NoError(t, json.NewDecoder(req.Body).Decode(&query))
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"schema_version": policy.SchemaVersionV4,
				"route_id": query.RouteID, "classifier_artifact_id": identity.ArtifactID, "classifier_artifact_sha256": identity.PackageSHA256,
				"predicted_label": "high", "class_order": identity.ClassOrder, "class_probabilities": map[string]float64{"high": 1}}))
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(classifier.Close)
	policySHA := strings.Repeat("d", 64)
	candidate := policyregistry.Candidate{HeadSnapshot: policyregistry.HeadSnapshot{Head: policyregistry.LaneHead{ClassifierRevisionURL: classifier.URL}},
		Release: policyregistry.Release{Classifier: identity, Policy: policyregistry.PolicyObject{SHA256: policySHA}},
		Policy: &rosterdata.Roster{SchemaVersion: rosterdata.SchemaVersionPolicyV1, ClassOrder: identity.ClassOrder,
			Clusters: map[string]rosterdata.Cluster{"high": {Arms: []string{providers.ProviderOpenAI + "/" + catalog.ModelGPT6Luna}}}}}
	available := map[string]struct{}{providers.ProviderOpenAI: {}}
	routers, err := hmmPolicySnapshotBuilder(available, []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding}, policySidecarAuthNone, time.Second, time.Second)(context.Background(), candidate)
	require.NoError(t, err)
	pinnedRuntime := &policyregistry.PinnedRuntime{Snapshot: &policyregistry.Snapshot{Candidate: candidate, Routers: routers}}
	ctx := pinnedRuntime.Bind(context.Background())
	decision, err := policyregistry.NewAdmittedRouter(router.StrategyHMMEmbedding, pinnedRuntime.Snapshot).Route(ctx, router.Request{PromptText: "Authored local composition check", ClientApp: "codex"})
	require.NoError(t, err)
	require.Equal(t, catalog.ModelGPT6Luna, decision.Model)
	require.Equal(t, policySHA, decision.Metadata.SelectionPolicySHA256)
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(map[string]struct{}{catalog.ModelGPT6Luna: {}}, available, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)
	var bindings []catalog.ProviderBinding
	for _, binding := range catalog.EnumerateBindings(decision.Model, available) {
		bindings = append(bindings, binding.ProviderBinding)
	}
	plan, err := plans.ResolveRouted(policy.RoutedResolutionRequest{Purpose: policy.PurposeOpenAIResponses, Decision: decision, Bindings: bindings})
	require.NoError(t, err)
	provider := &localFakeProvider{}
	executor, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: provider}), dispatch.WithSleep(func(context.Context, time.Duration) error { return nil }))
	require.NoError(t, err)
	result, err := executor.Run(ctx, inference.InvocationRequest{Purpose: inference.PurposeOpenAIResponses, RequestID: "synthetic-local"}, plan,
		dispatch.Buffered{Prepare: func(_ context.Context, attempt dispatch.Attempt) (providers.PreparedRequest, *http.Request, error) {
			body, marshalErr := json.Marshal(map[string]string{"model": attempt.Target.UpstreamID})
			return providers.PreparedRequest{Body: body}, httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)), marshalErr
		}}.Transport())
	require.NoError(t, err)
	require.Equal(t, 2, result.Outcome.AttemptCount)
	require.Equal(t, []string{catalog.ModelGPT6Luna, catalog.ModelGPT6Luna}, provider.models)
	require.Equal(t, catalog.ModelGPT6Luna, result.Summary.ServedTarget.CatalogID)
}

func TestLocalPinnedInitialRouterSkipsClusterConstructionAndRejectsFallback(t *testing.T) {
	t.Setenv("ROUTER_CLUSTER_VERSION", "authored-invalid-cluster-version")
	initial, embedderID, err := buildInitialRoutingStrategy(nil, true)
	require.NoError(t, err)
	require.Empty(t, embedderID)
	_, err = initial.Route(context.Background(), router.Request{PromptText: "Authored offline fallback refusal"})
	require.ErrorContains(t, err, "fallback strategy is unavailable")
	_, _, err = buildInitialRoutingStrategy(nil, false)
	require.Error(t, err, "normal mode still constructs and validates its cluster scorer")
}
