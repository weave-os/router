package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/eligibility"
)

func TestCatalog_EveryModelCarriesAValidSource(t *testing.T) {
	for _, m := range Models {
		require.NotEmptyf(t, m.Source, "model %q has no Source classification; open-source-only products cannot decide eligibility for it", m.ID)
		require.Truef(t, m.Source.Valid(), "model %q has unrecognized Source %q", m.ID, m.Source)
	}
}

func TestSourceValid(t *testing.T) {
	for _, source := range []Source{SourceOpenSource, SourceClosedSource, SourceUnknown} {
		assert.Truef(t, source.Valid(), "%q should be a recognized source", source)
	}
	for _, source := range []Source{"", "open", "oss", "closed", "Open_Source"} {
		assert.Falsef(t, source.Valid(), "%q should not be a recognized source", source)
	}
}

func TestSourceFor(t *testing.T) {
	source, known := SourceFor("claude-opus-4-7")
	require.True(t, known)
	assert.Equal(t, SourceClosedSource, source)

	source, known = SourceFor("z-ai/glm-5.3")
	require.True(t, known)
	assert.Equal(t, SourceOpenSource, source)

	// An ID with no catalog row reports unknown, never a permissive default.
	source, known = SourceFor("no-such-model")
	assert.False(t, known)
	assert.Equal(t, SourceUnknown, source)
}

func TestPermittedByAndCheckEligibility(t *testing.T) {
	max := eligibility.MaxOpenSourceOnly

	assert.True(t, PermittedBy(max, "z-ai/glm-5.3"))
	assert.NoError(t, CheckEligibility(max, "z-ai/glm-5.3"))

	for _, id := range []string{"claude-opus-4-7", "muse-spark-1.3", "not-in-the-catalog"} {
		assert.Falsef(t, PermittedBy(max, id), "%q should be ineligible for Max", id)
		assert.ErrorIsf(t, CheckEligibility(max, id), eligibility.ErrModelIneligible, "%q should be refused", id)
	}

	// An unrestricted boundary keeps pre-product behavior, including for IDs
	// the catalog does not know.
	unrestricted := eligibility.Unrestricted()
	assert.True(t, PermittedBy(unrestricted, "claude-opus-4-7"))
	assert.NoError(t, CheckEligibility(unrestricted, "not-in-the-catalog"))
}

func TestIneligibleIDsCoversEveryNonOpenSourceModel(t *testing.T) {
	ineligible := IneligibleIDs(eligibility.MaxOpenSourceOnly)
	assert.IsIncreasing(t, ineligible)
	assert.Len(t, ineligible, len(IDsWithSource(SourceClosedSource))+len(IDsWithSource(SourceUnknown)))
	for _, id := range ineligible {
		source, known := SourceFor(id)
		require.True(t, known)
		assert.NotEqual(t, SourceOpenSource, source)
	}
	for _, id := range IDsWithSource(SourceOpenSource) {
		assert.NotContains(t, ineligible, id)
	}
	assert.Empty(t, IneligibleIDs(eligibility.Unrestricted()))
}

func TestIDsWithSourceIsSortedAndPartitionsTheCatalog(t *testing.T) {
	total := 0
	for _, source := range []Source{SourceOpenSource, SourceClosedSource, SourceUnknown} {
		ids := IDsWithSource(source)
		assert.IsIncreasingf(t, ids, "IDsWithSource(%q) is not sorted", source)
		total += len(ids)
	}
	assert.Equal(t, len(Models), total)
	assert.NotEmpty(t, IDsWithSource(SourceOpenSource))
	assert.NotEmpty(t, IDsWithSource(SourceClosedSource))
}
