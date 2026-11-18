package selection_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"weave-os/router/internal/router/hmm/policycompiler"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"

	"github.com/stretchr/testify/require"
)

const (
	syntheticCanonicalPolicyPath   = "testdata/golden/policy_v1_synthetic.canonical.json"
	syntheticCanonicalPolicySHA256 = "272c38f74bd7377f96f1da048988b31071231e7b44ab16f9ac9829e9b73b4108"
	syntheticCanonicalPolicyBytes  = 3178
)

func independentSHA256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func TestSyntheticPolicyFileDigestIsPinned(t *testing.T) {
	source, err := os.ReadFile(syntheticPolicyPath)
	require.NoError(t, err)
	require.Equal(t, syntheticPolicySHA256, independentSHA256Hex(source))
	evidence, err := os.ReadFile(syntheticDomainEvidencePath)
	require.NoError(t, err)
	require.Equal(t, syntheticDomainEvidenceSHA256, independentSHA256Hex(evidence))
}

func TestSyntheticPolicyCanonicalBytesArePinned(t *testing.T) {
	want, err := os.ReadFile(syntheticCanonicalPolicyPath)
	require.NoError(t, err)
	require.Len(t, want, syntheticCanonicalPolicyBytes)
	require.Equal(t, syntheticCanonicalPolicySHA256, independentSHA256Hex(want))

	source, err := os.ReadFile(syntheticPolicyPath)
	require.NoError(t, err)
	roster, err := rosterdata.Parse(source)
	require.NoError(t, err)
	canonical, err := rosterdata.CanonicalBytes(roster)
	require.NoError(t, err)
	require.Equal(t, string(want), string(canonical))

	compiled, _, err := policycompiler.Compile(source, policycompiler.Options{})
	require.NoError(t, err)
	require.Equal(t, string(want), string(compiled))

	reparsed, err := selectionpolicy.Admit(want)
	require.NoError(t, err)
	require.Equal(t, syntheticCanonicalPolicySHA256, reparsed.SHA256())
	recanonical, err := rosterdata.CanonicalBytes(reparsed.Roster())
	require.NoError(t, err)
	require.Equal(t, string(want), string(recanonical))
}
