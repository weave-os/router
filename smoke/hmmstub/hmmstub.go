// Package hmmstub stands in for the HMM policy sidecar and its baked local
// release so CI fixtures can boot the real worker on its hmm_embedding default
// without classifier infrastructure. It classifies every turn into one group.
package hmmstub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

const (
	// ClassLabel is the only routing group the stub ever predicts.
	ClassLabel   = "low"
	registryRoot = "gs://hmm-stub-fixture/registry"
	policyJSON   = `{
  "schema_version":"hmm_go_selection_policy_v1",
  "class_order":["low"],
  "ranking":{"alpha":{"low":0.4},"alpha_min":{"low":0.1},"alpha_max":{"low":0.8},"quality_bias_neutral":0.7,"wii_score_version":"wii-v1","wii_normalization_sha256":"wii","wpi_score_version":"wpi-v1","wpi_normalization_sha256":"wpi"},
  "preferences":{"preferred_model_bonus":0.5,"subscription_bonus":0.35},
  "clusters":{"low":{"complexity_label":"low","arms":["openai/gpt-5.6-sol"],"cost_ref_usd":1,"latency_ref_ms":1,"arm_scores":{"openai/gpt-5.6-sol":1},"arm_indices":{"openai/gpt-5.6-sol":{"wii_v1":80,"wpi_v1":40}}}}
}`
)

func classifier() policyregistry.ClassifierIdentity {
	classOrder := []string{ClassLabel}
	return policyregistry.ClassifierIdentity{
		ArtifactID:     "hmm-stub",
		PackageSHA256:  strings.Repeat("a", 64),
		ImageDigest:    "sha256:" + strings.Repeat("b", 64),
		WireSchema:     policyregistry.ClassifierWireSchemaV4,
		ClassOrder:     classOrder,
		TaxonomySHA256: policyregistry.TaxonomyDigest(classOrder),
	}
}

// WriteRelease bakes a stable-lane release naming the stub classifier into dir,
// in the layout ROUTER_POLICY_RELEASE_DIR expects.
func WriteRelease(dir string) error {
	policyPayload := []byte(policyJSON)
	policySHA := policyregistry.Digest(policyPayload)
	release := policyregistry.Release{
		SchemaVersion: policyregistry.ReleaseSchemaV1,
		Classifier:    classifier(),
		Policy: policyregistry.PolicyObject{
			URI:           registryRoot + "/router_policy/v1/policies/sha256/" + policySHA + ".json",
			SHA256:        policySHA,
			SchemaVersion: rosterdata.SchemaVersionPolicyV1,
			Generation:    1,
		},
		Provenance: policyregistry.Provenance{SourceRevision: "hmm-stub", CreatedBy: "hmm-stub", CreatedAt: "2026-01-01T00:00:00Z"},
	}
	releasePayload, err := json.Marshal(release)
	if err != nil {
		return fmt.Errorf("encode stub release: %w", err)
	}
	releaseSHA := policyregistry.Digest(releasePayload)
	head := policyregistry.LaneHead{
		SchemaVersion:          policyregistry.LaneHeadSchemaV1,
		Environment:            policyregistry.EnvironmentProd,
		Lane:                   policyregistry.LaneStable,
		ReleaseURI:             registryRoot + "/router_policy/v1/releases/sha256/" + releaseSHA + ".json",
		ReleaseSHA256:          releaseSHA,
		ReleaseGeneration:      1,
		ClassifierRevisionURL:  "https://hmm-stub.invalid",
		ClassifierRevisionName: "hmm-stub",
		PromotedBy:             "hmm-stub",
		PromotedAt:             "2026-01-01T00:00:00Z",
		Reason:                 "CI fixture",
	}
	return policyregistry.WriteDirectoryRelease(dir, head, releasePayload, policyPayload)
}

// Handler serves the v4 classifier contract for the release WriteRelease bakes.
func Handler() http.Handler {
	identity := classifier()
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, policyclient.ClassifierHealth{
			SchemaVersion:         identity.WireSchema,
			ClassifierArtifactID:  identity.ArtifactID,
			ClassifierSHA256:      identity.PackageSHA256,
			ClassifierImageDigest: identity.ImageDigest,
			ClassifierClassOrder:  identity.ClassOrder,
			ClassifierTaxonomySHA: identity.TaxonomySHA256,
		})
	}
	mux.HandleFunc("GET /livez", health)
	mux.HandleFunc("GET /readyz", health)
	mux.HandleFunc("GET /capabilities", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, policy.Capabilities{SchemaVersion: policy.SchemaVersionV4})
	})
	classify := func(w http.ResponseWriter, request *http.Request) {
		var query struct {
			RouteID string `json:"route_id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&query); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"schema_version":             policy.SchemaVersionV4,
			"route_id":                   query.RouteID,
			"classifier_artifact_id":     identity.ArtifactID,
			"classifier_artifact_sha256": identity.PackageSHA256,
			"predicted_label":            ClassLabel,
			"class_order":                identity.ClassOrder,
			"class_probabilities":        map[string]float64{ClassLabel: 1},
		})
	}
	mux.HandleFunc("POST /route", classify)
	mux.HandleFunc("POST /preview", classify)
	noContent := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	mux.HandleFunc("POST /outcome", noContent)
	mux.HandleFunc("POST /feedback", noContent)
	return mux
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
