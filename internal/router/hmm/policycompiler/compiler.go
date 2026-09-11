// Package policycompiler turns reviewed HMM roster source into the only
// serving-policy schema accepted by the Go runtime.
package policycompiler

import (
	"errors"
	"fmt"

	"weave-os/router/internal/router/hmm/rosterdata"
)

var defaultClassOrder = []string{"low", "medium", "high", "maximum"}

// legacyPooledHarness is how reviewed roster source names the all-harness pin
// bucket that canonical policy calls rosterdata.HarnessAll.
const legacyPooledHarness = "pooled"

func canonicalHarness(harness string) rosterdata.Harness {
	if harness == legacyPooledHarness {
		return rosterdata.HarnessAll
	}
	return rosterdata.Harness(harness)
}

// CompilableSource reports whether reviewed roster source in the given schema
// carries everything Go selection needs. v6 and older lack WII/WPI provenance,
// so the serving loader may still parse them but Compile can never emit a policy.
func CompilableSource(schema rosterdata.SchemaVersion) bool {
	switch schema {
	case rosterdata.SchemaVersionV7, rosterdata.SchemaVersionV75C, rosterdata.SchemaVersionPolicyV1:
		return true
	}
	return false
}

// Options contains reviewed policy metadata. Offline evidence is provenance;
// it never changes membership or order inside Compile.
type Options struct {
	ClassOrder          []string
	PreferredModelBonus *float64
	SubscriptionBonus   *float64
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
	sourceSchema := roster.SchemaVersion
	if roster.SchemaVersion != rosterdata.SchemaVersionPolicyV1 {
		roster.SchemaVersion = rosterdata.SchemaVersionPolicyV1
	}
	manualPins := make(map[string]map[string][]string, len(roster.ManualPins))
	for sourceHarness, pinsByLabel := range roster.ManualPins {
		harness := canonicalHarness(sourceHarness)
		manualPins[string(harness)] = pinsByLabel
		for label, pins := range pinsByLabel {
			cluster, ok := roster.Clusters[label]
			if !ok {
				continue
			}
			if cluster.ManualPinsByHarness == nil {
				cluster.ManualPinsByHarness = make(map[rosterdata.Harness][]string)
			}
			if _, exists := cluster.ManualPinsByHarness[harness]; !exists {
				cluster.ManualPinsByHarness[harness] = append([]string(nil), pins...)
			}
			roster.Clusters[label] = cluster
		}
	}
	if len(manualPins) > 0 {
		roster.ManualPins = manualPins
	}
	for harness, priority := range roster.HarnessVendorPriority {
		for _, label := range priority.Clusters {
			cluster, ok := roster.Clusters[label]
			if !ok {
				continue
			}
			if cluster.PreferredVendorsByHarness == nil {
				cluster.PreferredVendorsByHarness = make(map[rosterdata.Harness][]string)
			}
			if _, exists := cluster.PreferredVendorsByHarness[harness]; !exists {
				cluster.PreferredVendorsByHarness[harness] = append([]string(nil), priority.Vendors...)
			}
			roster.Clusters[label] = cluster
		}
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
	preferences := roster.Preferences
	if sourceSchema != rosterdata.SchemaVersionPolicyV1 && preferences.PreferredModelBonus == 0 {
		preferences.PreferredModelBonus = 0.5
	}
	if sourceSchema != rosterdata.SchemaVersionPolicyV1 && preferences.SubscriptionBonus == 0 {
		preferences.SubscriptionBonus = 0.35
	}
	if options.PreferredModelBonus != nil {
		preferences.PreferredModelBonus = *options.PreferredModelBonus
	}
	if options.SubscriptionBonus != nil {
		preferences.SubscriptionBonus = *options.SubscriptionBonus
	}
	roster.Preferences = preferences
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
