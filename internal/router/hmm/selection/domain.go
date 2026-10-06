package selection

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"time"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/taskdomain"
)

// Domain is one independent work-category bit, not a complexity label.
type Domain = taskdomain.Domain

const (
	DomainUI    = taskdomain.UI
	DomainLogic = taskdomain.Logic
	DomainData  = taskdomain.Data
	DomainInfra = taskdomain.Infra
	DomainDocs  = taskdomain.Docs
)

// Benchmark is one AA evaluation a task-domain recipe may weight.
type Benchmark string

const (
	BenchmarkTerminalBench4       Benchmark = "terminalbench_v4_0"
	BenchmarkLongContextReasoning Benchmark = "lcr"
	BenchmarkIFBench              Benchmark = "ifbench"
	BenchmarkAnalystAgent         Benchmark = "analyst_agent"
	BenchmarkTerminalBenchScience Benchmark = "terminalbench_science"
	BenchmarkITBench              Benchmark = "itbench_sre"
)

const (
	domainSchemaVersion = "domain_wmi_evidence_v3"
	// DomainRecipeVersion names the weights below; evidence carries only data.
	DomainRecipeVersion = "domain_wmi_weighted_v1"
	// DomainInfluence is the share of the quality term a task's benchmark mix
	// may replace; at full coverage the quality term is 85% WII + 15% recipe.
	DomainInfluence = 0.15
)

// Sums run in these fixed orders so the float result is reproducible across
// replicas and matches the offline preview.
var (
	scoredDomainOrder = []Domain{DomainUI, DomainLogic, DomainData, DomainInfra}
	benchmarkOrder    = []Benchmark{
		BenchmarkTerminalBench4, BenchmarkLongContextReasoning, BenchmarkIFBench,
		BenchmarkAnalystAgent, BenchmarkTerminalBenchScience, BenchmarkITBench,
	}
)

// domainRecipes is the pinned benchmark mix per domain. Docs is deliberately
// absent: it neither corrects scores nor dilutes another active domain.
var domainRecipes = map[Domain]map[Benchmark]float64{
	DomainUI:    {BenchmarkTerminalBench4: 0.7, BenchmarkLongContextReasoning: 0.2, BenchmarkIFBench: 0.1},
	DomainLogic: {BenchmarkTerminalBench4: 0.7, BenchmarkLongContextReasoning: 0.2, BenchmarkIFBench: 0.1},
	DomainData: {
		BenchmarkTerminalBench4: 0.4, BenchmarkLongContextReasoning: 0.1,
		BenchmarkAnalystAgent: 0.3, BenchmarkTerminalBenchScience: 0.2,
	},
	DomainInfra: {BenchmarkTerminalBench4: 0.6, BenchmarkIFBench: 0.1, BenchmarkITBench: 0.3},
}

// DomainProfile is absent when the classifier cannot establish a human-turn profile.
// A present profile with no active bits is a distinct, valid prediction.
type DomainProfile = taskdomain.Profile

// DomainArmEvidence holds exact-effort normalized benchmark qualities. A
// benchmark the arm was never evaluated on is absent, not zero.
type DomainArmEvidence struct {
	GlobalWII  float64               `json:"global_wii"`
	WPI        float64               `json:"wpi"`
	Benchmarks map[Benchmark]float64 `json:"benchmarks"`
}

// DomainEvidence is version-bound, candidate-complete benchmark data. The
// recipe that weights it is owned here, not by the evidence producer.
type DomainEvidence struct {
	SchemaVersion           string                       `json:"schema_version"`
	SourceSnapshotSHA256    string                       `json:"source_snapshot_sha256"`
	SourceIngestDate        string                       `json:"source_ingest_date"`
	BenchmarkSnapshotSHA256 string                       `json:"benchmark_snapshot_sha256"`
	BenchmarkIngestDate     string                       `json:"benchmark_ingest_date"`
	RosterSHA256            string                       `json:"roster_sha256"`
	WIIScoreVersion         string                       `json:"wii_score_version"`
	WIINormalizationSHA256  string                       `json:"wii_normalization_sha256"`
	WPIScoreVersion         string                       `json:"wpi_score_version"`
	WPINormalizationSHA256  string                       `json:"wpi_normalization_sha256"`
	Arms                    map[string]DomainArmEvidence `json:"arms"`
}

// ParseDomainEvidence validates exact-arm coverage and roster/version binding
// before the quality correction can be computed. It never falls back to base effort.
func ParseDomainEvidence(payload []byte, roster *rosterdata.Roster) (*DomainEvidence, error) {
	var evidence DomainEvidence
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return nil, fmt.Errorf("parse domain evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("domain evidence has trailing content")
	}
	if err := rejectNullBenchmarks(payload); err != nil {
		return nil, err
	}
	if err := validateDomainEvidence(&evidence, roster); err != nil {
		return nil, err
	}
	return &evidence, nil
}

func validateDomainEvidence(evidence *DomainEvidence, roster *rosterdata.Roster) error {
	if roster == nil || roster.SHA256 == "" || evidence.RosterSHA256 != roster.SHA256 {
		return errors.New("domain evidence roster binding mismatch")
	}
	if !isSHA256(evidence.SourceSnapshotSHA256) || !isSHA256(evidence.BenchmarkSnapshotSHA256) || !isSHA256(evidence.RosterSHA256) ||
		!isSHA256(evidence.WIINormalizationSHA256) || !isSHA256(evidence.WPINormalizationSHA256) {
		return errors.New("domain evidence has invalid provenance")
	}
	for _, date := range []string{evidence.SourceIngestDate, evidence.BenchmarkIngestDate} {
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			return fmt.Errorf("domain evidence has invalid ingest date: %w", err)
		}
	}
	if evidence.SchemaVersion != domainSchemaVersion ||
		evidence.WIIScoreVersion != roster.Ranking.WIIScoreVersion ||
		evidence.WIINormalizationSHA256 != roster.Ranking.WIINormalizationSHA256 ||
		evidence.WPIScoreVersion != roster.Ranking.WPIScoreVersion ||
		evidence.WPINormalizationSHA256 != roster.Ranking.WPINormalizationSHA256 {
		return errors.New("domain evidence version binding mismatch")
	}
	if len(evidence.Arms) != len(roster.AllArms()) {
		return errors.New("domain evidence has incomplete candidate coverage")
	}
	for _, arm := range roster.AllArms() {
		cell, exists := evidence.Arms[arm]
		if !exists || !boundedIndex(cell.GlobalWII) || !boundedIndex(cell.WPI) {
			return fmt.Errorf("domain evidence missing valid exact arm %q", arm)
		}
		for benchmark, quality := range cell.Benchmarks {
			if !slices.Contains(benchmarkOrder, benchmark) || !boundedIndex(quality) {
				return fmt.Errorf("domain evidence has invalid %q quality for %q", benchmark, arm)
			}
		}
		for _, cluster := range roster.Clusters {
			if indices, found := cluster.ArmIndices[arm]; found && (math.Abs(indices.WII-cell.GlobalWII) > 1e-5 || math.Abs(indices.WPI-cell.WPI) > 1e-5) {
				return fmt.Errorf("domain indices mismatch for %q", arm)
			}
		}
	}
	for _, cluster := range roster.Clusters {
		for arm := range cluster.ArmScores {
			if _, exists := evidence.Arms[arm]; !exists {
				return fmt.Errorf("domain evidence missing scored arm %q", arm)
			}
		}
		for arm := range cluster.ArmIndices {
			if _, exists := evidence.Arms[arm]; !exists {
				return fmt.Errorf("domain evidence missing indexed arm %q", arm)
			}
		}
	}
	return nil
}

// rejectNullBenchmarks keeps an explicit null from decoding as a zero score:
// an unmeasured benchmark must be absent so it stays neutral.
func rejectNullBenchmarks(payload []byte) error {
	var decodedEvidence struct {
		Arms map[string]struct {
			Benchmarks map[Benchmark]*float64 `json:"benchmarks"`
		} `json:"arms"`
	}
	if err := json.Unmarshal(payload, &decodedEvidence); err != nil {
		return fmt.Errorf("parse domain evidence: %w", err)
	}
	for arm, cell := range decodedEvidence.Arms {
		for benchmark, quality := range cell.Benchmarks {
			if quality == nil {
				return fmt.Errorf("domain evidence has null %q quality for %q", benchmark, arm)
			}
		}
	}
	return nil
}

func boundedIndex(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func isSHA256(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

// ScoredDomains returns the active domains that carry a recipe. An unavailable
// or malformed profile, an all-zero profile and a docs-only profile all score
// at baseline.
func ScoredDomains(profile DomainProfile) []Domain {
	if profile.Validate() != nil {
		return nil
	}
	var active []Domain
	for _, domain := range scoredDomainOrder {
		if profile[domain] {
			active = append(active, domain)
		}
	}
	return active
}

// BenchmarkWeight is one benchmark's share of a domain recipe.
type BenchmarkWeight struct {
	Benchmark Benchmark `json:"benchmark"`
	Weight    float64   `json:"weight"`
}

// DomainRecipe is the ordered benchmark mix for one scored domain.
type DomainRecipe struct {
	Domain  Domain            `json:"domain"`
	Weights []BenchmarkWeight `json:"weights"`
}

// DomainRecipes returns the pinned recipes in scoring order for inspection.
func DomainRecipes() []DomainRecipe {
	recipes := make([]DomainRecipe, 0, len(scoredDomainOrder))
	for _, domain := range scoredDomainOrder {
		recipe := DomainRecipe{Domain: domain}
		for _, benchmark := range benchmarkOrder {
			if weight, weighted := domainRecipes[domain][benchmark]; weighted {
				recipe.Weights = append(recipe.Weights, BenchmarkWeight{Benchmark: benchmark, Weight: weight})
			}
		}
		recipes = append(recipes, recipe)
	}
	return recipes
}

// BenchmarkGap is one benchmark's term in an arm's task delta. Weight is
// averaged across the scored domains; Quality is nil when the arm was never
// evaluated, in which case Gap is zero.
type BenchmarkGap struct {
	Benchmark Benchmark `json:"benchmark"`
	Weight    float64   `json:"weight"`
	Quality   *float64  `json:"quality"`
	Gap       float64   `json:"gap"`
}

// BenchmarkGaps decomposes TaskQualityDelta into its per-benchmark terms.
func BenchmarkGaps(domains []Domain, arm DomainArmEvidence) []BenchmarkGap {
	if len(domains) == 0 {
		return nil
	}
	var gaps []BenchmarkGap
	for _, benchmark := range benchmarkOrder {
		weight := 0.0
		for _, domain := range domains {
			weight += domainRecipes[domain][benchmark]
		}
		if weight == 0 {
			continue
		}
		weight /= float64(len(domains))
		gap := BenchmarkGap{Benchmark: benchmark, Weight: weight}
		if quality, measured := arm.Benchmarks[benchmark]; measured {
			gap.Quality = &quality
			gap.Gap = weight * (quality - arm.GlobalWII)
		}
		gaps = append(gaps, gap)
	}
	return gaps
}

// TaskQualityDelta averages each scored domain's weighted benchmark gap over
// global WII. A missing benchmark contributes nothing and its weight is not
// redistributed, so an arm with no relevant scores keeps a zero delta.
func TaskQualityDelta(domains []Domain, arm DomainArmEvidence) float64 {
	delta := 0.0
	for _, gap := range BenchmarkGaps(domains, arm) {
		delta += gap.Gap
	}
	return delta
}
