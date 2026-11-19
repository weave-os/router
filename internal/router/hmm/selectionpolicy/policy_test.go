package selectionpolicy_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/policycompiler"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"
)

const (
	syntheticPolicyPath     = "../selection/testdata/golden/policy_v1_synthetic.json"
	syntheticCanonicalPath  = "../selection/testdata/golden/policy_v1_synthetic.canonical.json"
	compilerSourcePath      = "../rosterdata/testdata/roster_valid.json"
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

func TestAdmitAcceptsCheckedInPolicyV1WithItsLiteralDigest(t *testing.T) {
	for path, expectedSHA := range map[string]string{
		syntheticPolicyPath:    "2a384cad8c6b8e06c785b99e9bbc39079d177e78419abdd0a356658101415a31",
		syntheticCanonicalPath: "272c38f74bd7377f96f1da048988b31071231e7b44ab16f9ac9829e9b73b4108",
	} {
		t.Run(path, func(t *testing.T) {
			payload, err := os.ReadFile(path)
			require.NoError(t, err)
			admitted, err := selectionpolicy.Admit(payload)
			require.NoError(t, err)
			assert.Equal(t, expectedSHA, admitted.SHA256())
			assert.Equal(t, expectedSHA, admitted.Roster().SHA256)
		})
	}
}

func TestAdmittedRosterReencodesToTheCheckedInCanonicalBytes(t *testing.T) {
	payload, err := os.ReadFile(syntheticPolicyPath)
	require.NoError(t, err)
	canonical, err := os.ReadFile(syntheticCanonicalPath)
	require.NoError(t, err)

	admitted, err := selectionpolicy.Admit(payload)
	require.NoError(t, err)
	reencoded, err := rosterdata.CanonicalBytes(admitted.Roster())
	require.NoError(t, err)
	assert.Equal(t, string(canonical), string(reencoded))
}

func TestAdmitAcceptsTheCompiledCheckedInSourceWithItsLiteralDigest(t *testing.T) {
	source, err := os.ReadFile(compilerSourcePath)
	require.NoError(t, err)
	_, admitted, err := policycompiler.Compile(source, policycompiler.Options{ClassOrder: []string{"low", "maximum"}})
	require.NoError(t, err)
	assert.Equal(t, "2c3d0fd7d50fb577cca4c1cae37473fa90a1d2f24aa96d702e80f66caf6c95ef", admitted.SHA256())
}

func TestAdmitRejectsEachCheckedInNonPolicyFixtureForItsOwnReason(t *testing.T) {
	for path, expectedReason := range map[string]string{
		compilerSourcePath: `admit requires schema "hmm_go_selection_policy_v1", got "hmm_router_cluster_roster_v7"`,
		"../rosterdata/testdata/roster_unknown_arm.json":              "madeup/not-a-model: unknown_catalog_model",
		"../selection/testdata/parity_roster.json":                    "roster has 6 invalid arms",
		"../selection/testdata/golden/domain_evidence_synthetic.json": `unknown field "source_snapshot_sha256"`,
		"../../../api/admin/testdata/golden/hmm_roster.json":          `unknown field "release_id"`,
	} {
		t.Run(path, func(t *testing.T) {
			payload, err := os.ReadFile(path)
			require.NoError(t, err)
			_, err = selectionpolicy.Admit(payload)
			require.ErrorContains(t, err, expectedReason)
		})
	}
}

func TestAdmitRejectsStrictDecodeCatalogAndSchemaViolations(t *testing.T) {
	valid := minimalPolicyV1("openai/gpt-5.6-sol")
	admitted, err := selectionpolicy.Admit(valid)
	require.NoError(t, err)
	assert.Equal(t, "1e57fc65fc2ecb7ae9492015c1f49ae967add702944f7e5dec2127e2050cf6a4", admitted.SHA256())

	for name, testCase := range map[string]struct {
		payload        []byte
		expectedReason string
	}{
		"unknown field":       {[]byte(strings.Replace(string(valid), `"class_order"`, `"unexpected":true,"class_order"`, 1)), `unknown field "unexpected"`},
		"trailing document":   {append(append([]byte{}, valid...), []byte(`{}`)...), "multiple JSON values"},
		"unknown catalog arm": {minimalPolicyV1("madeup/not-a-model"), "madeup/not-a-model: unknown_catalog_model"},
		"empty payload":       {nil, "EOF"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := selectionpolicy.Admit(testCase.payload)
			require.ErrorContains(t, err, testCase.expectedReason)
		})
	}
}

func TestAdmittedPolicyIgnoresChangesToRostersItHandedOut(t *testing.T) {
	payload, err := os.ReadFile(syntheticPolicyPath)
	require.NoError(t, err)
	canonical, err := os.ReadFile(syntheticCanonicalPath)
	require.NoError(t, err)
	admitted, err := selectionpolicy.Admit(payload)
	require.NoError(t, err)

	handedOut := admitted.Roster()
	handedOut.ClassOrder[0] = "tampered"
	handedOut.SubscriptionLadders["tampered"] = map[rosterdata.Group]string{rosterdata.Group("low"): "madeup/not-a-model"}
	for label, cluster := range handedOut.Clusters {
		cluster.Arms[0] = "madeup/not-a-model"
		handedOut.Clusters[label] = cluster
	}
	handedOut.SHA256 = "tampered"
	payload[0] = 'x'

	reencoded, err := rosterdata.CanonicalBytes(admitted.Roster())
	require.NoError(t, err)
	assert.Equal(t, string(canonical), string(reencoded))
	assert.Equal(t, "2a384cad8c6b8e06c785b99e9bbc39079d177e78419abdd0a356658101415a31", admitted.SHA256())
	assert.Equal(t, "2a384cad8c6b8e06c785b99e9bbc39079d177e78419abdd0a356658101415a31", admitted.Roster().SHA256)
}
