package catalog

import (
	"fmt"
	"sort"

	"weave-os/router/internal/router/eligibility"
)

type Source = eligibility.Source

const (
	SourceOpenSource   = eligibility.SourceOpenSource
	SourceClosedSource = eligibility.SourceClosedSource
	SourceUnknown      = eligibility.SourceUnknown
)

func SourceFor(id string) (Source, bool) {
	model, known := ByID(id)
	if !known {
		return SourceUnknown, false
	}
	return model.Source, true
}

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

func PermittedBy(boundary eligibility.Boundary, id string) bool {
	if !boundary.Restricts() {
		return true
	}
	source, known := SourceFor(id)
	return known && boundary.PermitsSource(source)
}

func CheckEligibility(boundary eligibility.Boundary, id string) error {
	if PermittedBy(boundary, id) {
		return nil
	}
	if source, known := SourceFor(id); known {
		return fmt.Errorf("%q is %s and cannot be served on %s: %w", id, source, boundary.Product(), eligibility.ErrModelIneligible)
	}
	return fmt.Errorf("%q is not a catalog model and cannot be served on %s: %w", id, boundary.Product(), eligibility.ErrModelIneligible)
}

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
