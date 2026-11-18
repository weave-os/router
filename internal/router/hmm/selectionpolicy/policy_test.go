package selectionpolicy_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"
)

const (
	repositoryRoot          = "../../../.."
	syntheticPolicyPath     = "../selection/testdata/golden/policy_v1_synthetic.json"
	syntheticCanonicalPath  = "../selection/testdata/golden/policy_v1_synthetic.canonical.json"
	compilerSourceOnlyPath  = "../rosterdata/testdata/roster_valid.json"
	minimalPolicyV1Template = `{
  "schema_version":"hmm_go_selection_policy_v1",
  "class_order":["low"],
  "ranking":{"alpha":{"low":0.4},"alpha_min":{"low":0.1},"alpha_max":{"low":0.8},"quality_bias_neutral":0.7,"wii_score_version":"wii-v1","wii_normalization_sha256":"wii","wpi_score_version":"wpi-v1","wpi_normalization_sha256":"wpi"},
  "clusters":{"low":{"complexity_label":"low","arms":["ARM"],"cost_ref_usd":1,"latency_ref_ms":1,"arm_scores":{"ARM":1},"arm_indices":{"ARM":{"wii_v1":80,"wpi_v1":40}}}}
}`
)

func minimalPolicyV1(arm string) []byte {
	return []byte(strings.ReplaceAll(minimalPolicyV1Template, "ARM", arm))
}

func TestAdmitKeepsPolicyV1WireBytesAndDigest(t *testing.T) {
	payload, err := os.ReadFile(syntheticPolicyPath)
	require.NoError(t, err)
	canonical, err := os.ReadFile(syntheticCanonicalPath)
	require.NoError(t, err)

	admitted, err := selectionpolicy.Admit(payload)
	require.NoError(t, err)
	assert.Equal(t, rosterdata.SHA256Hex(payload), admitted.SHA256())
	assert.Equal(t, admitted.SHA256(), admitted.Roster().SHA256)
	reencoded, err := rosterdata.CanonicalBytes(admitted.Roster())
	require.NoError(t, err)
	assert.Equal(t, string(canonical), string(reencoded))
}

func TestAdmitRejectsWhatStrictDecodeCatalogOrSchemaRejected(t *testing.T) {
	compilerSource, err := os.ReadFile(compilerSourceOnlyPath)
	require.NoError(t, err)
	valid := minimalPolicyV1("openai/gpt-5.6-sol")
	_, err = selectionpolicy.Admit(valid)
	require.NoError(t, err)

	for name, payload := range map[string][]byte{
		"compiler source schema": compilerSource,
		"unknown field":          []byte(strings.Replace(string(valid), `"class_order"`, `"unexpected":true,"class_order"`, 1)),
		"trailing document":      append(append([]byte{}, valid...), []byte(`{}`)...),
		"unknown catalog arm":    minimalPolicyV1("madeup/not-a-model"),
		"empty payload":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := selectionpolicy.Admit(payload)
			require.Error(t, err)
		})
	}
}

func TestAdmitMatchesParseCatalogAndSchemaForEveryCheckedInRoster(t *testing.T) {
	root, err := os.OpenRoot(repositoryRoot)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	repository := root.FS()
	admittedCount := 0
	err = fs.WalkDir(repository, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		payload, err := fs.ReadFile(repository, path)
		if err != nil || !strings.Contains(string(payload), `"schema_version"`) {
			return err
		}
		expectedSHA, expectedAdmitted := parseCatalogAndSchema(payload)
		admitted, admitErr := selectionpolicy.Admit(payload)
		require.Equal(t, expectedAdmitted, admitErr == nil, "%s: %v", path, admitErr)
		if admitErr == nil {
			admittedCount++
			assert.Equal(t, expectedSHA, admitted.SHA256(), path)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Positive(t, admittedCount)
}

func parseCatalogAndSchema(payload []byte) (string, bool) {
	roster, err := rosterdata.Parse(payload)
	if err != nil || rosterdata.ValidateCatalog(roster) != nil || roster.SchemaVersion != rosterdata.SchemaVersionPolicyV1 {
		return "", false
	}
	return rosterdata.SHA256Hex(payload), true
}
