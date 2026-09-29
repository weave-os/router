package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/policycompiler"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
)

const draftPreviewSchema = "hmm_draft_roster_preview_v1"

type alphaFlags []string

func (values *alphaFlags) String() string { return strings.Join(*values, ",") }
func (values *alphaFlags) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type previewArm struct {
	Arm             string   `json:"arm"`
	OriginalRank    int      `json:"original_rank"`
	Score           *float64 `json:"score"`
	WII             *float64 `json:"wii"`
	WPI             *float64 `json:"wpi"`
	ManualPin       bool     `json:"manual_pin"`
	PreferredVendor bool     `json:"preferred_vendor"`
	Rank            int      `json:"rank"`
}

type previewCluster struct {
	Alpha       float64      `json:"alpha"`
	QualityBias *float64     `json:"quality_bias"`
	Arms        []previewArm `json:"arms"`
}

type previewGridPoint struct {
	QualityBias float64 `json:"quality_bias"`
	Alpha       float64 `json:"alpha"`
	Winner      *string `json:"winner"`
}

type draftPreview struct {
	Environment   string                        `json:"environment"`
	Source        string                        `json:"source"`
	SchemaVersion rosterdata.SchemaVersion      `json:"schema_version"`
	PreviewSchema string                        `json:"preview_schema_version"`
	Harness       *string                       `json:"harness"`
	QualityBias   *float64                      `json:"quality_bias"`
	Clusters      map[string]previewCluster     `json:"clusters"`
	QualityGrid   map[string][]previewGridPoint `json:"quality_grid,omitempty"`
}

func runPreview(args []string) error {
	flags := flag.NewFlagSet(string(commandPreview), flag.ContinueOnError)
	rosterPath := flags.String("roster-file", "", "local draft roster JSON")
	classOrderRaw := flags.String("class-order", "", "comma-separated classifier class order")
	environment := flags.String("environment", "", "optional caller environment label")
	harnessRaw := flags.String("harness", "", "request harness")
	qualityRaw := flags.String("quality-bias", "", "quality/price dial in [0,1]")
	gridSize := flags.Int("grid", 0, "quality/price winner sweep points (2-101)")
	var alphaValues alphaFlags
	flags.Var(&alphaValues, "alpha", "CLUSTER=VALUE override; repeat per cluster")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *rosterPath == "" || len(flags.Args()) != 0 {
		return errors.New("preview requires --roster-file and no positional arguments")
	}
	if *gridSize != 0 && (*gridSize < 2 || *gridSize > 101) {
		return errors.New("--grid must be between 2 and 101")
	}
	if *gridSize != 0 && len(alphaValues) > 0 {
		return errors.New("--grid cannot be combined with --alpha overrides")
	}
	harness, err := previewHarness(*harnessRaw)
	if err != nil {
		return err
	}
	var qualityBias *float64
	if *qualityRaw != "" {
		qualityBias, err = parseUnitFloat(*qualityRaw)
		if err != nil {
			return fmt.Errorf("--quality-bias: %w", err)
		}
	}
	source, err := filepath.Abs(*rosterPath)
	if err != nil {
		return fmt.Errorf("resolve roster path: %w", err)
	}
	payload, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read preview roster: %w", err)
	}
	roster, err := rosterdata.ParseValidated(payload)
	if err != nil {
		return fmt.Errorf("validate preview roster: %w", err)
	}
	alphaOverrides, err := parseAlphaFlags(alphaValues, roster.Clusters)
	if err != nil {
		return err
	}
	preview, err := renderDraftPreview(
		roster,
		parseList(*classOrderRaw),
		source,
		*environment,
		harness,
		qualityBias,
		alphaOverrides,
		*gridSize,
	)
	if err != nil {
		return err
	}
	return writeJSON(preview)
}

func previewHarness(raw string) (*string, error) {
	if raw == "" {
		return nil, nil
	}
	harness := strings.ReplaceAll(raw, "-", "_")
	switch rosterdata.Harness(harness) {
	case rosterdata.HarnessClaudeCode, rosterdata.HarnessCodex, rosterdata.HarnessPI, rosterdata.HarnessOpenCode:
		return &harness, nil
	default:
		return nil, fmt.Errorf("unknown harness %q", raw)
	}
}

func parseUnitFloat(raw string) (*float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return nil, fmt.Errorf("expected a finite number from 0 to 1, got %q", raw)
	}
	return &value, nil
}

func parseAlphaFlags(values []string, clusters map[string]rosterdata.Cluster) (map[string]float64, error) {
	overrides := make(map[string]float64, len(values))
	for _, raw := range values {
		label, number, found := strings.Cut(raw, "=")
		label = strings.TrimSpace(label)
		if !found || label == "" {
			return nil, fmt.Errorf("invalid --alpha %q; expected CLUSTER=VALUE", raw)
		}
		if _, exists := clusters[label]; !exists {
			return nil, fmt.Errorf("unknown --alpha cluster %q", label)
		}
		alpha, err := parseUnitFloat(strings.TrimSpace(number))
		if err != nil {
			return nil, fmt.Errorf("--alpha %q: %w", label, err)
		}
		overrides[label] = *alpha
	}
	return overrides, nil
}

func renderDraftPreview(
	roster *rosterdata.Roster,
	classOrder []string,
	source, environment string,
	harness *string,
	qualityBias *float64,
	alphaOverrides map[string]float64,
	gridSize int,
) (draftPreview, error) {
	sourceSchemaVersion := roster.SchemaVersion
	sourcePayload, err := json.Marshal(roster)
	if err != nil {
		return draftPreview{}, fmt.Errorf("encode preview roster: %w", err)
	}
	_, compiledRoster, err := policycompiler.Compile(sourcePayload, policycompiler.Options{ClassOrder: classOrder})
	if err != nil {
		return draftPreview{}, fmt.Errorf("compile preview roster: %w", err)
	}
	roster = compiledRoster
	harnessName := strings.ReplaceAll(harnessValue(harness), "-", "_")
	dynamic := qualityBias != nil || len(alphaOverrides) > 0 || gridSize > 0
	if dynamic {
		for label, cluster := range roster.Clusters {
			order, _ := selection.ArmOrder(cluster, harnessName)
			pins := previewManualPins(cluster, harnessName)
			for _, arm := range order {
				if containsString(pins, arm) {
					continue
				}
				if _, exists := cluster.ArmIndices[arm]; !exists {
					return draftPreview{}, fmt.Errorf("dynamic preview requires WII/WPI indices for cluster %q", label)
				}
			}
		}
	}
	effectiveQuality := qualityBias
	if dynamic && effectiveQuality == nil && len(alphaOverrides) == 0 {
		neutral := roster.Ranking.QualityBiasNeutral
		effectiveQuality = &neutral
	}
	preview := draftPreview{
		Environment: environment, Source: source, SchemaVersion: sourceSchemaVersion,
		PreviewSchema: draftPreviewSchema, Harness: harness, QualityBias: effectiveQuality,
		Clusters: make(map[string]previewCluster, len(roster.Clusters)),
	}
	for label := range roster.Clusters {
		clusterPreview, err := renderPreviewCluster(roster, label, harnessName, effectiveQuality, alphaOverrides)
		if err != nil {
			return draftPreview{}, err
		}
		preview.Clusters[label] = clusterPreview
	}
	if gridSize > 0 {
		preview.QualityGrid = make(map[string][]previewGridPoint, len(roster.Clusters))
		labels := make([]string, 0, len(roster.Clusters))
		for label := range roster.Clusters {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for index := range gridSize {
			pointQuality := float64(index) / float64(gridSize-1)
			for _, label := range labels {
				clusterPreview, err := renderPreviewCluster(roster, label, harnessName, &pointQuality, nil)
				if err != nil {
					return draftPreview{}, err
				}
				var winner *string
				if len(clusterPreview.Arms) > 0 {
					arm := clusterPreview.Arms[0].Arm
					winner = &arm
				}
				preview.QualityGrid[label] = append(preview.QualityGrid[label], previewGridPoint{
					QualityBias: roundSix(pointQuality), Alpha: clusterPreview.Alpha, Winner: winner,
				})
			}
		}
	}
	return preview, nil
}

func renderPreviewCluster(roster *rosterdata.Roster, label, harness string, qualityBias *float64, overrides map[string]float64) (previewCluster, error) {
	cluster := roster.Clusters[label]
	originalOrder, _ := selection.ArmOrder(cluster, harness)
	alpha := roster.Ranking.Alpha[label]
	if qualityBias != nil {
		alpha = selection.EffectiveAlpha(roster, label, *qualityBias)
	}
	selectionRoster := roster
	selectionQuality := qualityBias
	if override, exists := overrides[label]; exists {
		alpha = override
		// A local ranking copy lets the serving selector apply an exact alpha.
		// Zero is outside the neutral/static path, so it reads the override from
		// AlphaMin while retaining pins, vendor tiers, and tie breaking.
		copyRoster := *roster
		copyRoster.Ranking = roster.Ranking
		copyRoster.Ranking.AlphaMin = map[string]float64{label: override}
		selectionRoster = &copyRoster
		zero := 0.0
		selectionQuality = &zero
	}
	candidates := make(map[string]struct{}, len(originalOrder))
	for _, arm := range originalOrder {
		baseID, _ := hmm.SplitEffort(arm)
		candidates[baseID] = struct{}{}
	}
	_, scoresByGroup, _, ordersByGroup, _ := selection.SelectGroupsWithPreferences(
		selectionRoster, []selection.Group{{Label: label}}, harness, candidates,
		selectionQuality, nil, nil, nil,
	)
	positions := make(map[string]int, len(originalOrder))
	for index, arm := range originalOrder {
		positions[arm] = index + 1
	}
	pins := previewManualPins(cluster, harness)
	preferredVendors := cluster.PreferredVendorsByHarness[rosterdata.Harness(harness)]
	if len(preferredVendors) == 0 {
		preferredVendors = cluster.PreferredVendorsByHarness[rosterdata.Harness(strings.ReplaceAll(harness, "-", "_"))]
	}
	preview := previewCluster{Alpha: roundSix(alpha), QualityBias: qualityBias, Arms: make([]previewArm, 0, len(originalOrder))}
	for index, arm := range ordersByGroup[label] {
		var score, wii, wpi *float64
		if value, exists := scoresByGroup[label][arm]; exists {
			rounded := roundSix(float64(value))
			score = &rounded
		}
		if indices, exists := cluster.ArmIndices[arm]; exists {
			wii, wpi = &indices.WII, &indices.WPI
		}
		vendor, _, _ := strings.Cut(arm, "/")
		preview.Arms = append(preview.Arms, previewArm{
			Arm: arm, OriginalRank: positions[arm], Score: score, WII: wii, WPI: wpi,
			ManualPin: containsString(pins, arm), PreferredVendor: containsString(preferredVendors, vendor), Rank: index + 1,
		})
	}
	return preview, nil
}

func previewManualPins(cluster rosterdata.Cluster, harness string) []string {
	pins := append([]string(nil), cluster.ManualPinsByHarness[rosterdata.HarnessAll]...)
	return append(pins, cluster.ManualPinsByHarness[rosterdata.Harness(harness)]...)
}

func harnessValue(harness *string) string {
	if harness == nil {
		return ""
	}
	return *harness
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func roundSix(value float64) float64 { return math.Round(value*1e6) / 1e6 }
