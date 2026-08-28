// Package rosterdata decodes and validates the generated HMM roster JSON as
// declarative data. Nothing serves from a loaded roster yet; this is intended
// to replace the roster embedded in the immutable sidecar artifact package.
package rosterdata

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"workweave/router/internal/router/hmm"
)

// Roster is the parsed generated roster document. Unknown top-level fields
// are tolerated; the fields here are the ones the router consumes.
type Roster struct {
	SchemaVersion string             `json:"schema_version"`
	Ranking       Ranking            `json:"ranking"`
	Clusters      map[string]Cluster `json:"clusters"`
}

// Ranking carries the ranking metadata the roster builder used; Alpha is the
// per-cluster WMI blend weight.
type Ranking struct {
	Alpha map[string]float64 `json:"alpha"`
}

// Cluster is one complexity cluster's ordered arm roster and reference costs.
type Cluster struct {
	ComplexityLabel     string              `json:"complexity_label"`
	Arms                []string            `json:"arms"`
	ArmsByHarness       map[string][]string `json:"arms_by_harness"`
	MembershipByHarness map[string][]string `json:"membership_by_harness"`
	CostRefUSD          float64             `json:"cost_ref_usd"`
	LatencyRefMS        float64             `json:"latency_ref_ms"`
	ArmScores           map[string]float64  `json:"arm_scores"`
}

// AllArms returns every distinct arm ID referenced by the roster — cluster
// arms, per-harness arms, and per-harness membership — in sorted order.
func (r *Roster) AllArms() []string {
	seen := make(map[string]struct{})
	for _, cluster := range r.Clusters {
		for _, arm := range cluster.Arms {
			seen[arm] = struct{}{}
		}
		for _, arms := range cluster.ArmsByHarness {
			for _, arm := range arms {
				seen[arm] = struct{}{}
			}
		}
		for _, arms := range cluster.MembershipByHarness {
			for _, arm := range arms {
				seen[arm] = struct{}{}
			}
		}
	}
	arms := make([]string, 0, len(seen))
	for arm := range seen {
		arms = append(arms, arm)
	}
	sort.Strings(arms)
	return arms
}

// Parse decodes and schema-validates a roster document. It does not check
// arms against the model catalog; Load does.
func Parse(data []byte) (*Roster, error) {
	var roster Roster
	if err := json.Unmarshal(data, &roster); err != nil {
		return nil, fmt.Errorf("rosterdata: parse roster: %w", err)
	}
	if err := validateSchema(&roster); err != nil {
		return nil, fmt.Errorf("rosterdata: invalid roster: %w", err)
	}
	return &roster, nil
}

// Load reads and fully validates the roster at path, including catalog
// validation of every arm via hmm.ValidateRosterIDs.
func Load(path string) (*Roster, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rosterdata: read roster %q: %w", path, err)
	}
	roster, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if diagnostics := hmm.ValidateRosterIDs(roster.AllArms()); len(diagnostics) > 0 {
		lines := make([]string, 0, len(diagnostics))
		for _, d := range diagnostics {
			lines = append(lines, fmt.Sprintf("%s: %s", d.RosterID, d.Reason))
		}
		return nil, fmt.Errorf("rosterdata: roster %q has %d invalid arms: %s", path, len(diagnostics), strings.Join(lines, "; "))
	}
	return roster, nil
}

func validateSchema(r *Roster) error {
	if r.SchemaVersion == "" {
		return fmt.Errorf("missing schema_version")
	}
	if len(r.Clusters) == 0 {
		return fmt.Errorf("no clusters")
	}
	for label, cluster := range r.Clusters {
		if len(cluster.Arms) == 0 {
			return fmt.Errorf("cluster %q has no arms", label)
		}
		if cluster.CostRefUSD <= 0 {
			return fmt.Errorf("cluster %q has non-positive cost_ref_usd", label)
		}
		if cluster.LatencyRefMS <= 0 {
			return fmt.Errorf("cluster %q has non-positive latency_ref_ms", label)
		}
		for _, arm := range cluster.Arms {
			if _, ok := cluster.ArmScores[arm]; !ok {
				return fmt.Errorf("cluster %q arm %q has no arm_scores entry", label, arm)
			}
		}
		if _, ok := r.Ranking.Alpha[label]; !ok {
			return fmt.Errorf("cluster %q has no ranking.alpha entry", label)
		}
	}
	return nil
}
