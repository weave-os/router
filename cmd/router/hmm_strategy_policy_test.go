package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

func TestHMMQwenSelectsItsPinnedRosterThroughTheSharedSidecar(t *testing.T) {
	identity := policyregistry.ClassifierIdentity{
		ArtifactID: "synthetic-hmm", PackageSHA256: strings.Repeat("a", 64),
		ImageDigest: "sha256:" + strings.Repeat("b", 64), WireSchema: policy.SchemaVersionV4,
		ClassOrder: []string{"high"}, TaxonomySHA256: strings.Repeat("c", 64),
		Variants: map[router.Strategy]policy.ClassifierArtifact{
			router.StrategyHMMQwen: {ArtifactID: "synthetic-qwen", PackageSHA256: strings.Repeat("d", 64)},
		},
	}
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/readyz":
			require.NoError(t, json.NewEncoder(w).Encode(policyclient.ClassifierHealth{
				SchemaVersion: identity.WireSchema, ClassifierArtifactID: identity.ArtifactID,
				ClassifierSHA256: identity.PackageSHA256, ClassifierImageDigest: identity.ImageDigest,
				ClassifierClassOrder: identity.ClassOrder, ClassifierTaxonomySHA: identity.TaxonomySHA256,
				ClassifierVariants: identity.Variants,
			}))
		case "/capabilities":
			require.NoError(t, json.NewEncoder(w).Encode(policy.Capabilities{SchemaVersion: policy.SchemaVersionV4}))
		case "/route":
			var query struct {
				Strategy router.Strategy `json:"strategy"`
				RouteID  string          `json:"route_id"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&query))
			artifactID, artifactSHA := identity.ArtifactID, identity.PackageSHA256
			if variant, found := identity.Variants[query.Strategy]; found {
				artifactID, artifactSHA = variant.ArtifactID, variant.PackageSHA256
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"schema_version": policy.SchemaVersionV4, "route_id": query.RouteID,
				"classifier_artifact_id": artifactID, "classifier_artifact_sha256": artifactSHA,
				"predicted_label": "high", "class_order": identity.ClassOrder,
				"class_probabilities": map[string]float64{"high": 1},
			}))
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(sidecar.Close)
	roster := func(arm string) *rosterdata.Roster {
		return &rosterdata.Roster{SchemaVersion: rosterdata.SchemaVersionPolicyV1, ClassOrder: identity.ClassOrder,
			Clusters: map[string]rosterdata.Cluster{"high": {Arms: []string{arm}}}}
	}
	defaultPolicySHA, qwenPolicySHA := strings.Repeat("e", 64), strings.Repeat("f", 64)
	candidate := policyregistry.Candidate{
		HeadSnapshot: policyregistry.HeadSnapshot{Head: policyregistry.LaneHead{ClassifierRevisionURL: sidecar.URL}},
		Release:      policyregistry.Release{Classifier: identity, Policy: policyregistry.PolicyObject{SHA256: defaultPolicySHA}},
		Policy:       roster("openai/" + catalog.ModelGPT6Luna),
		StrategyPolicies: map[router.Strategy]policyregistry.StrategyPolicy{
			router.StrategyHMMQwen: {Reference: policyregistry.PolicyObject{SHA256: qwenPolicySHA}, Roster: roster(providers.ProviderGoogle + "/" + router.ModelGemini4Argon)},
		},
	}
	build := hmmPolicySnapshotBuilder(map[string]struct{}{providers.ProviderOpenAI: {}, providers.ProviderGoogle: {}},
		[]router.Strategy{router.StrategyHMM, router.StrategyHMMQwen}, "none", time.Second, time.Second)
	routers, err := build(context.Background(), candidate)
	require.NoError(t, err)
	for _, strategy := range []router.Strategy{router.StrategyHMM, router.StrategyHMMQwen} {
		decision, err := routers[strategy].Route(context.Background(), router.Request{PromptText: "Inspect the synthetic fixture", ClientApp: "codex"})
		require.NoError(t, err)
		expectedModel, expectedSHA := catalog.ModelGPT6Luna, defaultPolicySHA
		if strategy == router.StrategyHMMQwen {
			expectedModel, expectedSHA = router.ModelGemini4Argon, qwenPolicySHA
		}
		require.Equal(t, expectedModel, decision.Model)
		require.NotNil(t, decision.Metadata)
		require.Equal(t, expectedSHA, decision.Metadata.SelectionPolicySHA256)
	}
}
