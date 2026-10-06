package selection

import (
	"errors"
	"fmt"

	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
)

// DomainPreviewBenchmark is one benchmark's term in an arm's score correction.
type DomainPreviewBenchmark struct {
	BenchmarkGap
	Contribution float64 `json:"contribution"`
}

// DomainPreviewArm compares an arm's neutral baseline with its task-corrected score.
type DomainPreviewArm struct {
	Arm           string  `json:"arm"`
	BaselineRank  int     `json:"baseline_rank"`
	EffectiveRank int     `json:"effective_rank"`
	GlobalWII     float64 `json:"global_wii"`
	WPI           float64 `json:"wpi"`
	BaselineScore float32 `json:"baseline_score"`
	Correction    float32 `json:"correction"`
	AdjustedScore float32 `json:"adjusted_score"`
	// Scored is false for an arm, such as an unmeasured pin, the selector orders
	// without a score; its score fields are then zero and carry no meaning.
	Scored     bool                     `json:"scored"`
	Benchmarks []DomainPreviewBenchmark `json:"benchmarks"`
}

// DomainPreview is the live selector's within-cluster order for one task profile.
type DomainPreview struct {
	Alpha            float64            `json:"alpha"`
	Influence        float64            `json:"influence"`
	BaselineWinner   string             `json:"baseline_winner"`
	NumericWinner    string             `json:"numeric_winner"`
	EffectiveWinner  string             `json:"effective_winner"`
	WinnerSuppressed bool               `json:"winner_suppressed"`
	Arms             []DomainPreviewArm `json:"arms"`
}

// PreviewDomainRanking runs the serving selector with every arm of one cluster
// eligible, at the neutral quality preference and without per-user bonuses,
// once without and once with the task profile. Pins and harness vendor
// priority apply exactly as in serving.
func PreviewDomainRanking(roster *rosterdata.Roster, evidence *DomainEvidence, label, harness string, profile DomainProfile) (DomainPreview, error) {
	if roster == nil {
		return DomainPreview{}, errors.New("domain preview requires a roster")
	}
	cluster, exists := roster.Clusters[label]
	if !exists {
		return DomainPreview{}, fmt.Errorf("cluster %q is not in the policy", label)
	}
	if evidence == nil {
		return DomainPreview{}, errors.New("domain preview requires evidence")
	}
	if err := validateDomainEvidence(evidence, roster); err != nil {
		return DomainPreview{}, err
	}
	baselineOrder, _ := ArmOrder(cluster, harness)
	if len(baselineOrder) == 0 {
		return DomainPreview{}, fmt.Errorf("cluster %q has no arms", label)
	}
	candidates := make(map[string]struct{}, len(baselineOrder))
	for _, arm := range baselineOrder {
		baseID, _ := hmm.SplitEffort(arm)
		candidates[baseID] = struct{}{}
	}
	groups := []Group{{Label: label}}
	_, _, components, orders, _ := SelectGroupsWithDomainPreferences(roster, groups, harness, candidates, nil, nil, evidence, profile)
	effectiveOrder := orders[label]
	effectiveRanks := make(map[string]int, len(effectiveOrder))
	for rank, arm := range effectiveOrder {
		effectiveRanks[arm] = rank + 1
	}
	domains := ScoredDomains(profile)
	alpha := roster.Ranking.Alpha[label]
	preview := DomainPreview{
		Alpha: alpha, BaselineWinner: baselineOrder[0], EffectiveWinner: effectiveOrder[0],
		Arms: make([]DomainPreviewArm, 0, len(baselineOrder)),
	}
	if len(domains) > 0 {
		preview.Influence = DomainInfluence
	}
	numericLeader := -1
	for position, arm := range baselineOrder {
		cell := evidence.Arms[arm]
		score, scored := components[label][arm]
		previewArm := DomainPreviewArm{
			Arm: arm, BaselineRank: position + 1, EffectiveRank: effectiveRanks[arm],
			GlobalWII: cell.GlobalWII, WPI: cell.WPI, Scored: scored,
			BaselineScore: score.BaseScore, Correction: score.TaskDomainCorrection, AdjustedScore: score.TotalScore,
		}
		for _, gap := range BenchmarkGaps(domains, cell) {
			previewArm.Benchmarks = append(previewArm.Benchmarks, DomainPreviewBenchmark{BenchmarkGap: gap, Contribution: alpha * DomainInfluence * gap.Gap})
		}
		preview.Arms = append(preview.Arms, previewArm)
		if scored && (numericLeader < 0 || previewArm.AdjustedScore > preview.Arms[numericLeader].AdjustedScore) {
			numericLeader = position
		}
	}
	if numericLeader < 0 {
		return DomainPreview{}, fmt.Errorf("cluster %q has no scored arms", label)
	}
	preview.NumericWinner = preview.Arms[numericLeader].Arm
	preview.WinnerSuppressed = preview.NumericWinner != preview.EffectiveWinner
	return preview, nil
}
