package policyregistry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
)

const directoryPolicyJSON = `{
  "schema_version":"hmm_go_selection_policy_v1",
  "class_order":["low"],
  "ranking":{"alpha":{"low":0.4},"alpha_min":{"low":0.1},"alpha_max":{"low":0.8},"quality_bias_neutral":0.7,"wii_score_version":"wii-v1","wii_normalization_sha256":"wii","wpi_score_version":"wpi-v1","wpi_normalization_sha256":"wpi"},
  "preferences":{"preferred_model_bonus":0.5,"subscription_bonus":0.35},
  "clusters":{"low":{"complexity_label":"low","arms":["openai/gpt-5.6-sol"],"cost_ref_usd":1,"latency_ref_ms":1,"arm_scores":{"openai/gpt-5.6-sol":1},"arm_indices":{"openai/gpt-5.6-sol":{"wii_v1":80,"wpi_v1":40}}}}
}`

type bakedRegistry struct {
	dir        string
	policyPath string
}

func writeBakedRegistry(t *testing.T) bakedRegistry {
	t.Helper()
	dir := t.TempDir()
	policyPayload := []byte(directoryPolicyJSON)
	policySHA := policyregistry.Digest(policyPayload)

	release := validRelease()
	release.Classifier.ClassOrder = []string{"low"}
	release.Classifier.TaxonomySHA256 = policyregistry.TaxonomyDigest(release.Classifier.ClassOrder)
	release.Policy.SHA256 = policySHA
	release.Policy.URI = testRegistryRoot + "/router_policy/v1/policies/sha256/" + policySHA + ".json"
	releasePayload, err := json.Marshal(release)
	require.NoError(t, err)
	releaseSHA := policyregistry.Digest(releasePayload)

	head := policyregistry.LaneHead{
		SchemaVersion: policyregistry.LaneHeadSchemaV1,
		Environment:   policyregistry.EnvironmentProd, Lane: policyregistry.LaneStable,
		ReleaseURI:    testRegistryRoot + "/router_policy/v1/releases/sha256/" + releaseSHA + ".json",
		ReleaseSHA256: releaseSHA, ReleaseGeneration: 7,
		ClassifierRevisionURL:  "https://router-hmm-sidecar-00042-abc.a.run.app",
		ClassifierRevisionName: "router-hmm-sidecar-00042-abc",
		PromotedBy:             "operator", PromotedAt: "2026-09-10T12:00:00Z", Reason: "stable promote",
	}
	require.Error(t, policyregistry.WriteDirectoryRelease(dir, head, append(releasePayload, ' '), policyPayload))
	require.NoError(t, policyregistry.WriteDirectoryRelease(dir, head, releasePayload, policyPayload))
	policyPath := filepath.Join(dir, "router_policy", "v1", "policies", "sha256", policySHA+".json")
	return bakedRegistry{dir: dir, policyPath: policyPath}
}

func capturingBuilder(urls *[]string) policyregistry.Builder {
	return func(_ context.Context, candidate policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		*urls = append(*urls, candidate.HeadSnapshot.Head.ClassifierRevisionURL)
		return map[router.Strategy]router.Router{router.StrategyHMM: fixedRouter{model: "local"}}, nil
	}
}

func TestDirectoryRegistryServesBakedReleaseAgainstLocalSidecar(t *testing.T) {
	baked := writeBakedRegistry(t)
	registry, err := policyregistry.NewDirectoryRegistry(baked.dir, "http://hmm-sidecar:8093/")
	require.NoError(t, err)
	assert.Equal(t, testRegistryRoot, registry.RootURI())

	var urls []string
	manager, err := policyregistry.NewManager(registry, registry.Environment(), registry.Lane(), capturingBuilder(&urls), slog.Default())
	require.NoError(t, err)
	require.NoError(t, manager.Refresh(context.Background()))

	require.NotNil(t, manager.Active())
	assert.Equal(t, []string{"http://hmm-sidecar:8093"}, urls)

	require.NoError(t, manager.Refresh(context.Background()))
	assert.Len(t, urls, 1, "an unchanged baked head must not rebuild the snapshot")
}

func TestDirectoryRegistryKeepsPromotedClassifierWithoutOverride(t *testing.T) {
	baked := writeBakedRegistry(t)
	registry, err := policyregistry.NewDirectoryRegistry(baked.dir, "")
	require.NoError(t, err)

	snapshot, err := registry.ReadHead(context.Background(), policyregistry.EnvironmentProd, policyregistry.LaneStable)
	require.NoError(t, err)
	assert.Equal(t, "https://router-hmm-sidecar-00042-abc.a.run.app", snapshot.Head.ClassifierRevisionURL)

	_, err = registry.ReadHead(context.Background(), policyregistry.EnvironmentProd, policyregistry.LaneBeta)
	assert.ErrorIs(t, err, policyregistry.ErrNotFound)
}

func TestDirectoryRegistryRejectsTamperedPolicyBytes(t *testing.T) {
	baked := writeBakedRegistry(t)
	tampered := strings.Replace(directoryPolicyJSON, `"cost_ref_usd":1`, `"cost_ref_usd":2`, 1)
	require.NoError(t, os.WriteFile(baked.policyPath, []byte(tampered), 0o600))
	registry, err := policyregistry.NewDirectoryRegistry(baked.dir, "http://hmm-sidecar:8093")
	require.NoError(t, err)

	var urls []string
	manager, err := policyregistry.NewManager(registry, registry.Environment(), registry.Lane(), capturingBuilder(&urls), slog.Default())
	require.NoError(t, err)
	err = manager.Refresh(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match its digest")
	assert.Nil(t, manager.Active())
	assert.Empty(t, urls)
}

func TestDirectoryRegistryFailsClosedOnMissingHeadOrBadSidecarURL(t *testing.T) {
	_, err := policyregistry.NewDirectoryRegistry(t.TempDir(), "")
	assert.ErrorIs(t, err, policyregistry.ErrNotFound)

	baked := writeBakedRegistry(t)
	for _, raw := range []string{"hmm-sidecar:8093", "ftp://hmm-sidecar", "http://user:pass@hmm-sidecar"} {
		_, err := policyregistry.NewDirectoryRegistry(baked.dir, raw)
		assert.Error(t, err, raw)
	}
}

func TestWriteDirectoryReleaseKeepsWorkingHeadWhenObjectWriteFails(t *testing.T) {
	baked := writeBakedRegistry(t)
	headPath := filepath.Join(baked.dir, "head.json")
	headBefore, err := os.ReadFile(headPath)
	require.NoError(t, err)
	var head policyregistry.LaneHead
	require.NoError(t, json.Unmarshal(headBefore, &head))
	releaseBefore, err := os.ReadFile(filepath.Join(baked.dir, "router_policy", "v1", "releases", "sha256", head.ReleaseSHA256+".json"))
	require.NoError(t, err)
	var release policyregistry.Release
	require.NoError(t, json.Unmarshal(releaseBefore, &release))
	release.Provenance.CreatedBy = "next-export"
	releasePayload, err := json.Marshal(release)
	require.NoError(t, err)
	head.ReleaseSHA256 = policyregistry.Digest(releasePayload)
	head.ReleaseURI = testRegistryRoot + "/router_policy/v1/releases/sha256/" + head.ReleaseSHA256 + ".json"
	require.NoError(t, os.MkdirAll(filepath.Join(baked.dir, "router_policy", "v1", "releases", "sha256", head.ReleaseSHA256+".json"), 0o755))
	policyPayload, err := os.ReadFile(baked.policyPath)
	require.NoError(t, err)

	require.Error(t, policyregistry.WriteDirectoryRelease(baked.dir, head, releasePayload, policyPayload))

	headAfter, err := os.ReadFile(headPath)
	require.NoError(t, err)
	assert.Equal(t, headBefore, headAfter)
	_, err = policyregistry.NewDirectoryRegistry(baked.dir, "")
	require.NoError(t, err)
}
