package policyregistry_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/policy"
)

func TestCheckedInCandidateRequirementsMatchRouterCode(t *testing.T) {
	expected, err := policyregistry.RouterCandidateRequirementsJSON()
	require.NoError(t, err)
	actual, err := os.ReadFile(filepath.Join("..", "..", policyregistry.RouterCandidateRequirementsPath))
	require.NoError(t, err)
	require.Equal(t, string(expected), string(actual), "run make generate-candidate-requirements")

	decoder := json.NewDecoder(bytes.NewReader(actual))
	decoder.DisallowUnknownFields()
	var declaration policyregistry.RouterCandidateRequirementsDeclaration
	require.NoError(t, decoder.Decode(&declaration))
	require.Equal(t, policyregistry.RouterCandidateRequirementsDeclarationV1, declaration.SchemaVersion)
	require.Equal(t, policy.SupportedClassifierAPIMajors(), declaration.Requirements.SupportedAPIMajors, "publishers read the supported majors from this file")
}
