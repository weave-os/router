package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"
)

const syntheticPolicySourcePath = "../../internal/router/hmm/selection/testdata/golden/policy_v1_synthetic.json"

func runCompileCheckSummary(t *testing.T, sourcePath string) (string, error) {
	t.Helper()
	summaryPath := filepath.Join(t.TempDir(), "summary.json")
	summaryFile, err := os.Create(summaryPath)
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = summaryFile
	compileErr := runCompile([]string{"--source", sourcePath, "--check"})
	os.Stdout = stdout
	require.NoError(t, summaryFile.Close())
	if compileErr != nil {
		return "", compileErr
	}
	summaryPayload, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	var summary struct {
		PolicySHA256 string `json:"policy_sha256"`
		Output       string `json:"output"`
	}
	require.NoError(t, json.Unmarshal(summaryPayload, &summary))
	assert.Empty(t, summary.Output)
	return summary.PolicySHA256, nil
}

func TestRunCompileCheckReportsTheAdmittedPolicyDigest(t *testing.T) {
	source, err := os.ReadFile(syntheticPolicySourcePath)
	require.NoError(t, err)
	roster, err := rosterdata.Parse(source)
	require.NoError(t, err)
	canonical, err := rosterdata.CanonicalBytes(roster)
	require.NoError(t, err)
	admitted, err := selectionpolicy.Admit(canonical)
	require.NoError(t, err)

	digest, err := runCompileCheckSummary(t, syntheticPolicySourcePath)
	require.NoError(t, err)
	assert.Equal(t, admitted.SHA256(), digest)
}

func TestRunCompileCheckRejectsWhatAdmissionRejects(t *testing.T) {
	source, err := os.ReadFile(syntheticPolicySourcePath)
	require.NoError(t, err)
	roster, err := rosterdata.Parse(source)
	require.NoError(t, err)
	arm := roster.AllArms()[0]
	sourcePath := filepath.Join(t.TempDir(), "unknown_arm.json")
	require.NoError(t, os.WriteFile(sourcePath, []byte(strings.ReplaceAll(string(source), `"`+arm+`"`, `"madeup/not-a-model"`)), 0o600))

	_, err = runCompileCheckSummary(t, sourcePath)
	require.ErrorContains(t, err, "unknown_catalog_model")
}
