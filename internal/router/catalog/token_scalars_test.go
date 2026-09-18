package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The scalar table's keys are copied verbatim into WorkWeave, where a key that
// no longer names a catalog model does not fail — the lookup just falls back to
// unit scalars and the cell silently stops applying. This is the only place the
// keys can be checked against Models, so a typo or a retired model has to be
// caught here.
func TestRouterTokenScalarTable_KeysNameCatalogModels(t *testing.T) {
	assertCatalogModel := func(id, role string) {
		t.Helper()
		m, ok := ByID(id)
		if !assert.Truef(t, ok, "token scalar %s %q is not a catalog model", role, id) {
			return
		}
		// ByID falls back to a date-stripped lookup, so a dated or otherwise
		// near-miss key resolves to a different ID than it names.
		assert.Equalf(t, id, m.ID, "token scalar %s %q resolves to catalog model %q — use the canonical ID", role, id, m.ID)
	}

	for servedModelID, scalarsByBaseline := range routerTokenScalarTable {
		assertCatalogModel(servedModelID, "row")
		for baselineModelID := range scalarsByBaseline {
			assertCatalogModel(baselineModelID, "column")
		}
	}
}

func TestRouterTokenScalarTable_RatiosArePositive(t *testing.T) {
	for servedModelID, scalarsByBaseline := range routerTokenScalarTable {
		for baselineModelID, scalars := range scalarsByBaseline {
			for _, kind := range []struct {
				name  string
				ratio float64
			}{
				{"Input", scalars.Input},
				{"Output", scalars.Output},
				{"CacheWrite", scalars.CacheWrite},
				{"CacheRead", scalars.CacheRead},
			} {
				assert.Greaterf(t, kind.ratio, 0.0, "%s -> %s has non-positive %s ratio", servedModelID, baselineModelID, kind.name)
			}
		}
	}
}

// A row that covers only some baselines is a silent partial comparison: the
// uncovered columns fall back to unit scalars, so one org's chart is corrected
// and another's is not, with nothing to distinguish them.
func TestRouterTokenScalarTable_EveryRowCoversTheSameBaselines(t *testing.T) {
	var reference []string
	var referenceRow string
	for servedModelID, scalarsByBaseline := range routerTokenScalarTable {
		baselines := make([]string, 0, len(scalarsByBaseline))
		for baselineModelID := range scalarsByBaseline {
			baselines = append(baselines, baselineModelID)
		}
		if reference == nil {
			reference, referenceRow = baselines, servedModelID
			continue
		}
		assert.ElementsMatchf(t, reference, baselines, "row %q covers different baselines than row %q", servedModelID, referenceRow)
	}
}
