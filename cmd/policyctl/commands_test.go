package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func releaseWithClassifier() policyregistry.Release {
	return policyregistry.Release{Classifier: policyregistry.ClassifierIdentity{
		ArtifactID:    "hmm_router_sidecar_package_64f27f963b2da6f6",
		PackageSHA256: "64f27f963b2da6f6394d4677f466c6f89a53fbad857b33581bc8be2ed01ac066",
		ImageDigest:   "sha256:3de4d22f3b895ade91d7eb2f7377586b050260c93d5e016bcbfde52ef2c5dba2",
	}}
}

func TestValidateClassifierRevisionIdentityAcceptsMatchingRevision(t *testing.T) {
	release := releaseWithClassifier()
	require.NoError(t, validateClassifierRevisionIdentity(release, classifierRevisionIdentity{
		ArtifactID:    release.Classifier.ArtifactID,
		PackageSHA256: release.Classifier.PackageSHA256,
		ImageDigest:   release.Classifier.ImageDigest,
	}))
}

func TestValidateClassifierRevisionIdentityRejectsMismatches(t *testing.T) {
	release := releaseWithClassifier()
	matching := classifierRevisionIdentity{
		ArtifactID:    release.Classifier.ArtifactID,
		PackageSHA256: release.Classifier.PackageSHA256,
		ImageDigest:   release.Classifier.ImageDigest,
	}
	cases := map[string]struct {
		mutate func(*classifierRevisionIdentity)
		want   string
	}{
		"artifact": {func(id *classifierRevisionIdentity) { id.ArtifactID = "other_package" }, "classifier artifact"},
		"package":  {func(id *classifierRevisionIdentity) { id.PackageSHA256 = "deadbeef" }, "classifier package digest"},
		"image":    {func(id *classifierRevisionIdentity) { id.ImageDigest = "sha256:0000" }, "classifier image digest"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			observed := matching
			tc.mutate(&observed)
			err := validateClassifierRevisionIdentity(release, observed)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func promoteArgs(extra ...string) []string {
	return append([]string{
		"--registry", "gs://unused",
		"--environment", "staging-01",
		"--lane", "stable",
		"--release", "abc",
		"--expected-generation", "0",
		"--classifier-revision-url", "https://rp-abc---sidecar.a.run.app",
		"--classifier-revision-name", "router-hmm-sidecar-policy-abc",
		"--reason", "test",
		"--actor", "test",
	}, extra...)
}

func TestRunPromoteRejectsUnknownClassifierVerification(t *testing.T) {
	err := runPromote(context.Background(), promoteArgs("--classifier-verification", "trust-me"), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown --classifier-verification "trust-me"`)
}

func TestRunPromoteControlPlaneVerificationRequiresObservedIdentity(t *testing.T) {
	err := runPromote(context.Background(), promoteArgs(
		"--classifier-verification", "control-plane",
		"--classifier-artifact-id", "hmm_router_sidecar_package_64f27f963b2da6f6",
	), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires --classifier-artifact-id, --classifier-package-sha256, and --classifier-image-digest")
}
