package catalog

import (
	"fmt"
	"sort"

	"weave-os/router/internal/router/eligibility"
)

// Source classifies how a model's weights are published. Aliased rather than
// redeclared so the catalog stays the only place model rows are written while
// the type itself lives in the leaf package that products depend on.
type Source = eligibility.Source

const (
	SourceOpenSource   = eligibility.SourceOpenSource
	SourceClosedSource = eligibility.SourceClosedSource
	SourceUnknown      = eligibility.SourceUnknown
)

// SourceFor returns a model's source classification. The second result is
// false for an unknown catalog ID, which callers enforcing a product boundary
// must treat as ineligible rather than as unrestricted.
func SourceFor(id string) (Source, bool) {
	model, known := ByID(id)
	if !known {
		return SourceUnknown, false
	}
	return model.Source, true
}

// IDsWithSource returns the catalog IDs carrying the given classification,
// sorted for deterministic callers.
func IDsWithSource(source Source) []string {
	var ids []string
	for _, model := range Models {
		if model.Source == source {
			ids = append(ids, model.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// PermittedBy reports whether a boundary may dispatch a catalog ID. An ID with
// no catalog row is refused: a boundary cannot vouch for a model it cannot
// classify.
func PermittedBy(boundary eligibility.Boundary, id string) bool {
	if !boundary.Restricts() {
		return true
	}
	source, known := SourceFor(id)
	return known && boundary.PermitsSource(source)
}

// CheckEligibility returns nil when the boundary may dispatch the catalog ID,
// else an error wrapping eligibility.ErrModelIneligible.
func CheckEligibility(boundary eligibility.Boundary, id string) error {
	if PermittedBy(boundary, id) {
		return nil
	}
	if source, known := SourceFor(id); known {
		return fmt.Errorf("%q is %s and cannot be served on %s: %w", id, source, boundary.Product(), eligibility.ErrModelIneligible)
	}
	return fmt.Errorf("%q is not a catalog model and cannot be served on %s: %w", id, boundary.Product(), eligibility.ErrModelIneligible)
}

// IneligibleIDs returns every catalog ID the boundary refuses, sorted. Callers
// desugar it into the hard per-request exclusion set so routers that build
// their own candidate pools never score an ineligible model. Empty for an
// unrestricted boundary.
func IneligibleIDs(boundary eligibility.Boundary) []string {
	if !boundary.Restricts() {
		return nil
	}
	var ids []string
	for _, model := range Models {
		if !boundary.PermitsSource(model.Source) {
			ids = append(ids, model.ID)
		}
	}
	sort.Strings(ids)
	return ids
}
