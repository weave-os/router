// Package policycompiler turns reviewed HMM roster source into the only
// serving-policy schema accepted by the Go runtime.
package policycompiler

import (
	"errors"
	"fmt"

	"weave-os/router/internal/router/hmm/rosterdata"
)

var defaultClassOrder = []string{"low", "medium", "high", "maximum"}

// Options contains reviewed policy metadata. Offline evidence is provenance;
// it never changes membership or order inside Compile.
type Options struct {
	ClassOrder          []string
	PreferredModelBonus float64
	SubscriptionBonus   float64
	SourceRevision      string
	EvidenceURI         string
	EvidenceSHA256      string
}

// Compile converts strict source JSON to canonical Go-owned serving-policy bytes.
func Compile(source []byte, options Options) ([]byte, *rosterdata.Roster, error) {
	roster, err := rosterdata.Parse(source)
	if err != nil {
		return nil, nil, err
	}
	if roster.SchemaVersion != rosterdata.SchemaVersionPolicyV1 {
		roster.SchemaVersion = rosterdata.SchemaVersionPolicyV1
	}
	classOrder := append([]string(nil), options.ClassOrder...)
	if len(classOrder) == 0 {
		if len(roster.ClassOrder) > 0 {
			classOrder = append(classOrder, roster.ClassOrder...)
		} else if hasDefaultTaxonomy(roster) {
			classOrder = append(classOrder, defaultClassOrder...)
		} else {
			return nil, nil, errors.New("policycompiler: class order is required for a non-standard taxonomy")
		}
	}
	roster.ClassOrder = classOrder
	preferredBonus := options.PreferredModelBonus
	if preferredBonus == 0 {
		preferredBonus = 0.5
	}
	subscriptionBonus := options.SubscriptionBonus
	if subscriptionBonus == 0 {
		subscriptionBonus = 0.35
	}
	roster.Preferences = rosterdata.PreferencePolicy{
		PreferredModelBonus: preferredBonus,
		SubscriptionBonus:   subscriptionBonus,
	}
	roster.Provenance = rosterdata.Provenance{
		SourceRevision: options.SourceRevision,
		EvidenceURI:    options.EvidenceURI,
		EvidenceSHA256: options.EvidenceSHA256,
	}
	if err := rosterdata.ValidateCatalog(roster); err != nil {
		return nil, nil, err
	}
	canonical, err := rosterdata.CanonicalBytes(roster)
	if err != nil {
		return nil, nil, fmt.Errorf("policycompiler: canonical policy: %w", err)
	}
	return canonical, roster, nil
}

func hasDefaultTaxonomy(roster *rosterdata.Roster) bool {
	if len(roster.Clusters) != len(defaultClassOrder) {
		return false
	}
	for _, label := range defaultClassOrder {
		if _, exists := roster.Clusters[label]; !exists {
			return false
		}
	}
	return true
}
