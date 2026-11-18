package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/rosterdata"
)

func TestTaskDomainEvidencePreflight(t *testing.T) {
	const fixtureDir = "../../internal/router/hmm/selection/testdata/golden/"
	source, err := os.ReadFile(syntheticPolicySourcePath)
	require.NoError(t, err)
	canonical, err := os.ReadFile(fixtureDir + "policy_v1_synthetic.canonical.json")
	require.NoError(t, err)
	evidence, err := os.ReadFile(fixtureDir + "domain_evidence_synthetic.json")
	require.NoError(t, err)
	canonicalSHA := rosterdata.SHA256Hex(canonical)
	sourceSHA := rosterdata.SHA256Hex(source)
	require.NotEqual(t, sourceSHA, canonicalSHA)

	compileCheck := func(t *testing.T, evidencePath string) error {
		t.Helper()
		digest, err := runCompileCheckSummary(t, syntheticPolicySourcePath, "--task-domain-evidence", evidencePath)
		if err == nil {
			assert.Equal(t, canonicalSHA, digest)
		}
		return err
	}
	publish := func(t *testing.T, evidencePath string) error {
		t.Helper()
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
		return runPublish(t.Context(), []string{
			"--policy", syntheticPolicySourcePath,
			"--classifier-artifact-id", "synthetic",
			"--classifier-package-sha256", strings.Repeat("a", 64),
			"--classifier-image-digest", "sha256:" + strings.Repeat("b", 64),
			"--source-revision", "synthetic",
			"--task-domain-evidence", evidencePath,
		})
	}

	for _, tc := range []struct {
		name      string
		evidence  string
		wantError string
	}{
		{name: "canonical policy", evidence: strings.ReplaceAll(string(evidence), sourceSHA, canonicalSHA)},
		{name: "source digest", evidence: string(evidence), wantError: "domain evidence roster binding mismatch"},
		{name: "invalid JSON", evidence: "{", wantError: "parse domain evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "evidence.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.evidence), 0o600))
			if tc.wantError != "" {
				require.ErrorContains(t, compileCheck(t, path), tc.wantError)
				require.ErrorContains(t, publish(t, path), tc.wantError)
				return
			}
			require.NoError(t, compileCheck(t, path))
			require.ErrorContains(t, publish(t, path), "create router policy storage client")
		})
	}
}
