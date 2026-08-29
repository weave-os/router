package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_ValidRosterPasses(t *testing.T) {
	var out strings.Builder
	ok, err := run(filepath.Join("testdata", "roster_valid.json"), &out)
	require.NoError(t, err)
	assert.True(t, ok, "valid roster must pass: %s", out.String())
	assert.Contains(t, out.String(), "0 invalid")
}

func TestRun_InvalidArmFailsLoudly(t *testing.T) {
	var out strings.Builder
	ok, err := run(filepath.Join("testdata", "roster_invalid.json"), &out)
	require.NoError(t, err)
	assert.False(t, ok, "roster with an unknown arm must fail")
	assert.Contains(t, out.String(), "newvendor/model-x")
	assert.Contains(t, out.String(), "unknown_catalog_model")
}

func TestRun_MissingFileErrors(t *testing.T) {
	var out strings.Builder
	_, err := run(filepath.Join("testdata", "does_not_exist.json"), &out)
	require.Error(t, err)
}

func TestRosterArms_FlatArray(t *testing.T) {
	arms, err := rosterArms([]byte(`["a/b", "c/d"]`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a/b", "c/d"}, arms)
}

func TestRosterArms_SidecarRosterResponse(t *testing.T) {
	arms, err := rosterArms([]byte(`{"roster_ids": ["a/b", "c/d"], "clusters": {"balanced": ["a/b"], "max": ["c/d"]}}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a/b", "c/d"}, arms)
}

func TestRosterArms_StringListClusters(t *testing.T) {
	arms, err := rosterArms([]byte(`{"clusters": {"balanced": ["a/b", "c/d"], "max": ["c/d"]}}`))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a/b", "c/d"}, arms)
}

func TestRosterArms_ObjectClusters(t *testing.T) {
	arms, err := rosterArms([]byte(`{"clusters": {"balanced": {"arms": ["a/b"]}}}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a/b"}, arms)
}

func TestRosterArms_NoArmsErrors(t *testing.T) {
	_, err := rosterArms([]byte(`{"clusters": {}}`))
	require.Error(t, err)
}
