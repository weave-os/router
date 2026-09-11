// Package rosterdata decodes and validates the generated HMM roster JSON as
// declarative data, replacing the sidecar-embedded roster. It is the input to the
// router's deterministic arm selection; see docs/HMM_GO_SELECTION.md.
package rosterdata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"weave-os/router/internal/router/hmm"
)

type SchemaVersion string

const (
	SchemaVersionV6   SchemaVersion = "hmm_router_cluster_roster_v6"
	SchemaVersionV7   SchemaVersion = "hmm_router_cluster_roster_v7"
	SchemaVersionV75C SchemaVersion = "hmm_router_cluster_roster_v7_5c"
	// SchemaVersionPolicyV1 is the first Go-owned serving-policy contract.
	SchemaVersionPolicyV1 SchemaVersion = "hmm_go_selection_policy_v1"
)

// Harness identifies a request harness with policy-specific arm ordering.
type Harness string

const (
	HarnessAll        Harness = "*"
	HarnessClaudeCode Harness = "claude_code"
	HarnessCodex      Harness = "codex"
	HarnessPI         Harness = "pi"
)

// Roster is the complete Go-owned HMM serving policy.
type Roster struct {
	SchemaVersion         SchemaVersion                     `json:"schema_version"`
	Comment               string                            `json:"comment,omitempty"`
	ClassOrder            []string                          `json:"class_order,omitempty"`
	Ranking               Ranking                           `json:"ranking"`
	HarnessVendorPriority map[Harness]HarnessVendorPriority `json:"harness_vendor_priority,omitempty"`
	ManualPins            map[string]map[string][]string    `json:"manual_pins,omitempty"`
	ModePolicies          map[string]ModePolicy             `json:"mode_policies,omitempty"`
	Preferences           PreferencePolicy                  `json:"preferences,omitempty"`
	Provenance            Provenance                        `json:"provenance,omitempty"`
	Clusters              map[string]Cluster                `json:"clusters"`
	// SHA256 identifies the exact roster bytes when loaded from disk.
	SHA256 string `json:"-"`
}

// HarnessVendorPriority records legacy generated vendor-affinity provenance.
type HarnessVendorPriority struct {
	Vendors  []string `json:"vendors"`
	Clusters []string `json:"clusters"`
}

// ModePolicy constrains the classifier groups available in one router mode.
type ModePolicy struct {
	AllowedGroups []string `json:"allowed_groups"`
}

// PreferencePolicy bounds score bonuses that cannot affect hard eligibility.
type PreferencePolicy struct {
	PreferredModelBonus float64 `json:"preferred_model_bonus,omitempty"`
	SubscriptionBonus   float64 `json:"subscription_bonus,omitempty"`
}

// Provenance identifies the reviewed source and optional offline evidence.
type Provenance struct {
	SourceRevision string `json:"source_revision,omitempty"`
	EvidenceURI    string `json:"evidence_uri,omitempty"`
	EvidenceSHA256 string `json:"evidence_sha256,omitempty"`
}

// Ranking carries the ranking metadata the roster builder used; Alpha is the
// per-cluster WMI blend weight.
type Ranking struct {
	Metric                 string             `json:"metric,omitempty"`
	Alpha                  map[string]float64 `json:"alpha"`
	AlphaMin               map[string]float64 `json:"alpha_min"`
	AlphaMax               map[string]float64 `json:"alpha_max"`
	QualityBiasNeutral     float64            `json:"quality_bias_neutral"`
	WIIScoreVersion        string             `json:"wii_score_version"`
	WIINormalizationSHA256 string             `json:"wii_normalization_sha256"`
	WPIScoreVersion        string             `json:"wpi_score_version"`
	WPINormalizationSHA256 string             `json:"wpi_normalization_sha256"`
	AgenticIndexPage       string             `json:"agentic_index_page,omitempty"`
	FallbackMetric         string             `json:"fallback_metric,omitempty"`
	SnapshotDate           string             `json:"snapshot_date,omitempty"`
	Source                 string             `json:"source,omitempty"`
	WIIDescription         string             `json:"wii_v1,omitempty"`
	WMIFilter              *RankingFilter     `json:"wmi_filter,omitempty"`
}

// RankingFilter records the reviewed membership bounds used by the offline compiler.
type RankingFilter struct {
	MinimumScoreByCluster map[string]float64 `json:"minimum_score_by_cluster"`
	MinArmsPerCluster     int                `json:"min_arms_per_cluster"`
	MaxArmsPerCluster     int                `json:"max_arms_per_cluster"`
}

// ArmIndices are the immutable quality and price axes used to dynamically
// rank an arm. Both are absolute 0-100 indices; WPI is not a serving price.
type ArmIndices struct {
	WII float64 `json:"wii_v1"`
	WPI float64 `json:"wpi_v1"`
}

// Cluster is one complexity cluster's ordered arm roster and reference costs.
type Cluster struct {
	ComplexityLabel           string                `json:"complexity_label"`
	Arms                      []string              `json:"arms"`
	ArmsByHarness             map[Harness][]string  `json:"arms_by_harness"`
	MembershipByHarness       map[Harness][]string  `json:"membership_by_harness"`
	CostRefUSD                float64               `json:"cost_ref_usd"`
	LatencyRefMS              float64               `json:"latency_ref_ms"`
	ArmScores                 map[string]float64    `json:"arm_scores"`
	ArmIndices                map[string]ArmIndices `json:"arm_indices"`
	ManualPinsByHarness       map[Harness][]string  `json:"manual_pins_by_harness"`
	PreferredVendorsByHarness map[Harness][]string  `json:"preferred_vendors_by_harness"`
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&roster); err != nil {
		return nil, fmt.Errorf("rosterdata: parse roster: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("rosterdata: parse roster: multiple JSON values")
		}
		return nil, fmt.Errorf("rosterdata: parse roster: %w", err)
	}
	if err := validateSchema(&roster); err != nil {
		return nil, fmt.Errorf("rosterdata: invalid roster: %w", err)
	}
	return &roster, nil
}

// ParseValidated strictly parses policy bytes and validates every arm against
// the compiled model catalog.
func ParseValidated(data []byte) (*Roster, error) {
	roster, err := Parse(data)
	if err != nil {
		return nil, err
	}
	roster.SHA256 = SHA256Hex(data)
	if err := ValidateCatalog(roster); err != nil {
		return nil, err
	}
	return roster, nil
}

// SHA256Hex returns the lowercase hexadecimal SHA-256 digest of raw roster bytes.
func SHA256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// ValidateCatalog rejects serving-policy arms that cannot resolve through the
// router's compiled catalog mapping.
func ValidateCatalog(roster *Roster) error {
	if roster == nil {
		return errors.New("rosterdata: nil roster")
	}
	if diagnostics := hmm.ValidateRosterIDs(roster.AllArms()); len(diagnostics) > 0 {
		lines := make([]string, 0, len(diagnostics))
		for _, diagnostic := range diagnostics {
			lines = append(lines, fmt.Sprintf("%s: %s", diagnostic.RosterID, diagnostic.Reason))
		}
		return fmt.Errorf("rosterdata: roster has %d invalid arms: %s", len(diagnostics), strings.Join(lines, "; "))
	}
	return nil
}

// CanonicalBytes returns compact deterministic JSON for hashing and publishing.
func CanonicalBytes(roster *Roster) ([]byte, error) {
	if roster == nil {
		return nil, errors.New("rosterdata: nil roster")
	}
	if err := validateSchema(roster); err != nil {
		return nil, fmt.Errorf("rosterdata: invalid roster: %w", err)
	}
	payload, err := json.Marshal(roster)
	if err != nil {
		return nil, fmt.Errorf("rosterdata: marshal canonical roster: %w", err)
	}
	return payload, nil
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
	roster.SHA256 = SHA256Hex(data)
	if err := ValidateCatalog(roster); err != nil {
		return nil, fmt.Errorf("rosterdata: roster %q: %w", path, err)
	}
	return roster, nil
}

func validateSchema(r *Roster) error {
	switch r.SchemaVersion {
	case SchemaVersionV6, SchemaVersionV7, SchemaVersionV75C, SchemaVersionPolicyV1:
	case "":
		return fmt.Errorf("missing schema_version")
	default:
		return fmt.Errorf("unsupported schema_version %q", r.SchemaVersion)
	}
	if len(r.Clusters) == 0 {
		return fmt.Errorf("no clusters")
	}
	if r.SchemaVersion == SchemaVersionPolicyV1 {
		if err := validateGoPolicy(r); err != nil {
			return err
		}
	}
	for label, cluster := range r.Clusters {
		if strings.TrimSpace(label) == "" || cluster.ComplexityLabel != label {
			return fmt.Errorf("cluster %q has mismatched complexity_label %q", label, cluster.ComplexityLabel)
		}
		if len(cluster.Arms) == 0 {
			return fmt.Errorf("cluster %q has no arms", label)
		}
		if duplicate := firstDuplicate(cluster.Arms); duplicate != "" {
			return fmt.Errorf("cluster %q contains duplicate arm %q", label, duplicate)
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
		if r.SchemaVersion == SchemaVersionV7 || r.SchemaVersion == SchemaVersionV75C || r.SchemaVersion == SchemaVersionPolicyV1 {
			if err := validateDynamicCluster(r, label, cluster); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateGoPolicy(r *Roster) error {
	if len(r.ClassOrder) != len(r.Clusters) {
		return fmt.Errorf("class_order must name every cluster exactly once")
	}
	seenGroups := make(map[string]struct{}, len(r.ClassOrder))
	for _, label := range r.ClassOrder {
		if _, duplicate := seenGroups[label]; duplicate {
			return fmt.Errorf("class_order contains duplicate group %q", label)
		}
		if _, exists := r.Clusters[label]; !exists {
			return fmt.Errorf("class_order references unknown group %q", label)
		}
		seenGroups[label] = struct{}{}
	}
	if !finiteRange(r.Preferences.PreferredModelBonus, 0, 1) || !finiteRange(r.Preferences.SubscriptionBonus, 0, 1) {
		return fmt.Errorf("preference bonuses must be finite values in [0,1]")
	}
	for mode, modePolicy := range r.ModePolicies {
		if strings.TrimSpace(mode) == "" || len(modePolicy.AllowedGroups) == 0 {
			return fmt.Errorf("mode policy %q must allow at least one group", mode)
		}
		return fmt.Errorf("mode policy %q is not supported by Go selection", mode)
	}
	for label, cluster := range r.Clusters {
		for harness, arms := range cluster.ArmsByHarness {
			if duplicate := firstDuplicate(arms); duplicate != "" {
				return fmt.Errorf("cluster %q harness %q contains duplicate arm %q", label, harness, duplicate)
			}
		}
		for harness, arms := range cluster.MembershipByHarness {
			if !knownHarness(harness) {
				return fmt.Errorf("cluster %q has unknown membership harness %q", label, harness)
			}
			if duplicate := firstDuplicate(arms); duplicate != "" {
				return fmt.Errorf("cluster %q membership harness %q contains duplicate arm %q", label, harness, duplicate)
			}
		}
		for harness, arms := range cluster.ManualPinsByHarness {
			if !knownHarness(harness) {
				return fmt.Errorf("cluster %q has unknown pin harness %q", label, harness)
			}
			if duplicate := firstDuplicate(arms); duplicate != "" {
				return fmt.Errorf("cluster %q pin harness %q contains duplicate arm %q", label, harness, duplicate)
			}
		}
		for harness := range cluster.PreferredVendorsByHarness {
			if !knownHarness(harness) {
				return fmt.Errorf("cluster %q has unknown preferred-vendors harness %q", label, harness)
			}
		}
	}
	return nil
}

func firstDuplicate(values []string) string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return value
		}
		seen[value] = struct{}{}
	}
	return ""
}

func validateDynamicCluster(r *Roster, label string, cluster Cluster) error {
	if r.Ranking.WIIScoreVersion == "" || r.Ranking.WIINormalizationSHA256 == "" ||
		r.Ranking.WPIScoreVersion == "" || r.Ranking.WPINormalizationSHA256 == "" {
		return fmt.Errorf("v7 roster has incomplete WII/WPI provenance")
	}
	neutral := r.Ranking.QualityBiasNeutral
	defaultAlpha := r.Ranking.Alpha[label]
	minAlpha, hasMin := r.Ranking.AlphaMin[label]
	maxAlpha, hasMax := r.Ranking.AlphaMax[label]
	if !finiteUnit(neutral) || neutral <= 0 || neutral >= 1 {
		return fmt.Errorf("ranking.quality_bias_neutral must be inside (0,1)")
	}
	if !hasMin || !hasMax || !finiteUnit(minAlpha) || !finiteUnit(defaultAlpha) || !finiteUnit(maxAlpha) || minAlpha > defaultAlpha || defaultAlpha > maxAlpha {
		return fmt.Errorf("cluster %q has invalid alpha calibration", label)
	}
	globalPins := armSet(cluster.ManualPinsByHarness[HarnessAll])
	for _, arm := range cluster.Arms {
		indices, ok := cluster.ArmIndices[arm]
		if !ok {
			if _, globallyPinned := globalPins[arm]; globallyPinned {
				continue
			}
			return fmt.Errorf("cluster %q arm %q has no arm_indices entry", label, arm)
		}
		if !finiteRange(indices.WII, 0, 100) || !finiteRange(indices.WPI, 0, 100) {
			return fmt.Errorf("cluster %q arm %q has invalid WII/WPI indices", label, arm)
		}
	}
	for harness, arms := range cluster.ArmsByHarness {
		if !knownHarness(harness) {
			return fmt.Errorf("cluster %q has unknown harness %q", label, harness)
		}
		harnessPins := armSet(cluster.ManualPinsByHarness[harness])
		for _, arm := range arms {
			indices, ok := cluster.ArmIndices[arm]
			if !ok {
				_, globallyPinned := globalPins[arm]
				_, pinnedForHarness := harnessPins[arm]
				if globallyPinned || pinnedForHarness {
					continue
				}
				return fmt.Errorf("cluster %q harness %q arm %q has no arm_indices entry", label, harness, arm)
			}
			if !finiteRange(indices.WII, 0, 100) || !finiteRange(indices.WPI, 0, 100) {
				return fmt.Errorf("cluster %q harness %q arm %q has invalid WII/WPI indices", label, harness, arm)
			}
		}
	}
	return nil
}

func knownHarness(harness Harness) bool {
	switch harness {
	case HarnessAll, HarnessClaudeCode, HarnessCodex, HarnessPI:
		return true
	default:
		return false
	}
}

func armSet(arms []string) map[string]struct{} {
	set := make(map[string]struct{}, len(arms))
	for _, arm := range arms {
		set[arm] = struct{}{}
	}
	return set
}

func finiteUnit(value float64) bool { return finiteRange(value, 0, 1) }

func finiteRange(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}
