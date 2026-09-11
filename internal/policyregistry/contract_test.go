package policyregistry_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
)

const testRegistryRoot = "gs://weave_ml/weave_registry"

func TestReleaseValidationBindsClassifierTaxonomyAndPolicyPath(t *testing.T) {
	release := validRelease()
	require.NoError(t, release.Validate(testRegistryRoot))

	release.Classifier.ClassOrder = []string{"high", "low"}
	err := release.Validate(testRegistryRoot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "taxonomy digest")
}

func TestDecodeLaneHeadRejectsUnknownFieldsAndWrongFacet(t *testing.T) {
	payload := []byte(`{
  "schema_version":"router_policy_lane_head_v1",
  "environment":"staging-01",
  "lane":"stable",
  "release_uri":"gs://weave_ml/weave_registry/router_policy/v1/releases/sha256/` + strings.Repeat("b", 64) + `.json",
  "release_sha256":"` + strings.Repeat("b", 64) + `",
  "release_generation":1,
  "classifier_revision_url":"https://classifier.example",
  "classifier_revision_name":"classifier-00001",
  "promoted_by":"operator",
  "promoted_at":"2026-09-10T12:00:00Z",
  "reason":"test",
  "python_roster":"forbidden"
}`)
	_, err := policyregistry.DecodeLaneHead(payload, testRegistryRoot, policyregistry.EnvironmentStaging, policyregistry.LaneStable)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")

	payload = []byte(`{
  "schema_version":"router_policy_lane_head_v1",
  "environment":"prod-01",
  "lane":"stable",
  "release_uri":"gs://weave_ml/weave_registry/router_policy/v1/releases/sha256/` + strings.Repeat("b", 64) + `.json",
  "release_sha256":"` + strings.Repeat("b", 64) + `",
  "release_generation":1,
  "classifier_revision_url":"https://classifier.example",
  "classifier_revision_name":"classifier-00001",
  "promoted_by":"operator",
  "promoted_at":"2026-09-10T12:00:00Z",
  "reason":"test"
}`)
	_, err = policyregistry.DecodeLaneHead(payload, testRegistryRoot, policyregistry.EnvironmentStaging, policyregistry.LaneStable)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "facet")
}

func TestReleaseIDChangesWhenEitherClassifierOrPolicyChanges(t *testing.T) {
	release := validRelease()
	first, err := policyregistry.ReleaseID(release)
	require.NoError(t, err)

	release.Classifier.ArtifactID = "classifier-2"
	classifierChanged, err := policyregistry.ReleaseID(release)
	require.NoError(t, err)
	assert.NotEqual(t, first, classifierChanged)

	release.Policy.SHA256 = strings.Repeat("c", 64)
	release.Policy.URI = testRegistryRoot + "/router_policy/v1/policies/sha256/" + release.Policy.SHA256 + ".json"
	second, err := policyregistry.ReleaseID(release)
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
}

func validRelease() policyregistry.Release {
	classOrder := []string{"low", "high"}
	policySHA := strings.Repeat("a", 64)
	return policyregistry.Release{
		SchemaVersion: policyregistry.ReleaseSchemaV1,
		Classifier: policyregistry.ClassifierIdentity{
			ArtifactID: "classifier-1", PackageSHA256: strings.Repeat("d", 64),
			ImageDigest: "sha256:" + strings.Repeat("e", 64), WireSchema: policyregistry.ClassifierWireSchemaV4,
			ClassOrder: classOrder, TaxonomySHA256: policyregistry.TaxonomyDigest(classOrder),
		},
		Policy: policyregistry.PolicyObject{
			URI:    testRegistryRoot + "/router_policy/v1/policies/sha256/" + policySHA + ".json",
			SHA256: policySHA, SchemaVersion: rosterdata.SchemaVersionPolicyV1, Generation: 1,
		},
		Provenance: policyregistry.Provenance{
			SourceRevision: "revision-1", CreatedBy: "operator", CreatedAt: "2026-09-10T12:00:00Z",
		},
	}
}
