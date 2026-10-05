package selection_test

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
)

func domainArmsForTest() map[string]selection.DomainArmEvidence {
	zero, hundred := 0.0, 100.0
	return map[string]selection.DomainArmEvidence{
		"vendor-a/quality": {GlobalWII: 90, WPI: 10, TerminalQuality: &zero},
		"vendor-b/cheap":   {GlobalWII: 55, WPI: 0, TerminalQuality: &hundred},
	}
}

func bindDomainRosterForTest(roster *rosterdata.Roster) {
	roster.SHA256 = strings.Repeat("a", 64)
	roster.Ranking.WIIScoreVersion = "authored-wii"
	roster.Ranking.WIINormalizationSHA256 = strings.Repeat("b", 64)
	roster.Ranking.WPIScoreVersion = "authored-wpi"
	roster.Ranking.WPINormalizationSHA256 = strings.Repeat("c", 64)
}

func domainEvidencePayloadForTest(t *testing.T, roster *rosterdata.Roster, arms any, logicWeight float64, ingestDate string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"schema_version": "domain_wmi_evidence_v2", "recipe_version": "domain_wmi_terminal_sparse_v1",
		"source_snapshot_sha256": strings.Repeat("d", 64), "source_ingest_date": ingestDate, "roster_sha256": roster.SHA256,
		"wii_score_version": "authored-wii", "wii_normalization_sha256": roster.Ranking.WIINormalizationSHA256,
		"wpi_score_version": "authored-wpi", "wpi_normalization_sha256": roster.Ranking.WPINormalizationSHA256,
		"recipes": map[string]any{
			"ui":    map[string]any{"influence": 0, "weights": map[string]float64{}},
			"logic": map[string]any{"influence": logicWeight, "weights": map[string]float64{"terminalbench_v2_1": 1}},
			"data":  map[string]any{"influence": 0, "weights": map[string]float64{}},
			"infra": map[string]any{"influence": 0.25, "weights": map[string]float64{"terminalbench_v2_1": 1}},
			"docs":  map[string]any{"influence": 0, "weights": map[string]float64{}},
		}, "arms": arms,
	})
	require.NoError(t, err)
	return payload
}

func domainEvidenceForTest(t *testing.T, roster *rosterdata.Roster) *selection.DomainEvidence {
	t.Helper()
	bindDomainRosterForTest(roster)
	evidence, err := selection.ParseDomainEvidence(domainEvidencePayloadForTest(t, roster, domainArmsForTest(), 0.15, "2026-09-28"), roster)
	require.NoError(t, err)
	return evidence
}

func TestSparseDomainScoresPreserveBaselineForAllMasks(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	groups := []selection.Group{{Label: "low"}}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	for mask := 0; mask < 32; mask++ {
		profile := selection.DomainProfile{}
		for bit, domain := range []selection.Domain{selection.DomainUI, selection.DomainLogic, selection.DomainData, selection.DomainInfra, selection.DomainDocs} {
			profile[domain] = mask&(1<<bit) != 0
		}
		pick, scores, _, order, ok := selection.SelectGroupsWithDomainPreferences(roster, groups, "", candidates, nil, nil, evidence, profile)
		require.True(t, ok)
		assert.ElementsMatch(t, roster.Clusters["low"].Arms, order["low"])
		beta := (0.15*boolFloat(profile[selection.DomainLogic]) + 0.25*boolFloat(profile[selection.DomainInfra])) / math.Max(1, float64(popcount(mask)))
		assert.InDelta(t, beta, selection.TerminalInfluence(profile), 1e-12)
		assert.InDelta(t, 30+0.4*beta*(0-90), scores["low"]["vendor-a/quality"], 1e-5)
		assert.InDelta(t, 25+0.4*beta*(100-55), scores["low"]["vendor-b/cheap"], 1e-5)
		if beta == 0 {
			assert.Equal(t, "vendor-a/quality", pick.Arm)
			assert.Equal(t, roster.Clusters["low"].Arms, order["low"])
		}
	}
	assert.Zero(t, selection.TerminalInfluence(nil))
	assert.Zero(t, selection.TerminalInfluence(selection.DomainProfile{selection.DomainInfra: true}))
	assert.Zero(t, selection.TerminalInfluence(selection.DomainProfile{
		selection.DomainUI: false, selection.DomainLogic: false, selection.DomainData: false,
		selection.DomainInfra: true, selection.Domain("unexpected"): false,
	}))
}

func TestSparseDomainWithoutEvidencePreservesExistingSelection(t *testing.T) {
	roster := dynamicRoster()
	groups := []selection.Group{{Label: "low"}}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	qualityBias := 0.8
	baselinePick, baselineScores, baselineComponents, baselineOrder, baselineOK := selection.SelectGroupsWithPreferences(
		roster, groups, "", candidates, &qualityBias, []string{"vendor-b/cheap"},
	)
	for _, profile := range []selection.DomainProfile{nil, fullProfile(selection.DomainInfra)} {
		pick, scores, components, order, ok := selection.SelectGroupsWithDomainPreferences(
			roster, groups, "", candidates, &qualityBias, []string{"vendor-b/cheap"}, nil, profile,
		)
		assert.Equal(t, baselineOK, ok)
		assert.Equal(t, baselinePick, pick)
		assert.Equal(t, baselineScores, scores)
		assert.Equal(t, baselineComponents, components)
		assert.Equal(t, baselineOrder, order)
	}
}

func TestSparseDomainRetainsPinVendorAndEligibility(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	cluster := roster.Clusters["low"]
	cluster.ManualPinsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessPI: {"vendor-a/quality"}}
	cluster.PreferredVendorsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessCodex: {"vendor-a"}}
	roster.Clusters["low"] = cluster
	profile := fullProfile(selection.DomainInfra)
	groups := []selection.Group{{Label: "low"}}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	pick, _, _, _, ok := selection.SelectGroupsWithDomainPreferences(roster, groups, "pi", candidates, nil, nil, evidence, profile)
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm)
	pick, _, _, _, ok = selection.SelectGroupsWithDomainPreferences(roster, groups, "codex", candidates, nil, nil, evidence, profile)
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm)
	pick, _, _, _, ok = selection.SelectGroupsWithDomainPreferences(roster, groups, "", candidateSet("vendor-b/cheap"), nil, nil, evidence, profile)
	require.True(t, ok)
	assert.Equal(t, "vendor-b/cheap", pick.Arm)
}

func TestSparseDomainZeroCorrectionKeepsNeutralOrder(t *testing.T) {
	roster := dynamicRoster()
	cluster := roster.Clusters["low"]
	cluster.ManualPinsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessPI: {"vendor-b/cheap"}}
	roster.Clusters["low"] = cluster
	evidence := domainEvidenceForTest(t, roster)
	for arm, cell := range evidence.Arms {
		quality := cell.GlobalWII
		cell.TerminalQuality = &quality
		evidence.Arms[arm] = cell
	}
	pick, scores, _, orders, ok := selection.SelectGroupsWithDomainPreferences(
		roster, []selection.Group{{Label: "low"}}, "pi", candidateSet("vendor-a/quality", "vendor-b/cheap"),
		nil, nil, evidence, fullProfile(selection.DomainInfra),
	)
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm)
	assert.Equal(t, cluster.Arms, orders["low"])
	assert.Equal(t, float32(30), scores["low"]["vendor-a/quality"])
}

func TestSparseDomainKeepsUserQualityPreference(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	profile := fullProfile(selection.DomainLogic)
	groups := []selection.Group{{Label: "low"}}
	priceHeavy := 0.0
	_, scores, _, _, ok := selection.SelectGroupsWithDomainPreferences(
		roster, groups, "", candidateSet("vendor-a/quality", "vendor-b/cheap"),
		&priceHeavy, nil, evidence, profile,
	)
	require.True(t, ok)
	alpha := roster.Ranking.AlphaMin["low"]
	assert.InDelta(t, alpha*90-(1-alpha)*10+alpha*0.15*(0-90), scores["low"]["vendor-a/quality"], 1e-5)
	assert.InDelta(t, alpha*55+alpha*0.15*(100-55), scores["low"]["vendor-b/cheap"], 1e-5)
}

func TestSparseEvidenceRejectsRecipeDriftAndMissingArm(t *testing.T) {
	roster := dynamicRoster()
	bindDomainRosterForTest(roster)
	omittedQuality := map[string]any{"global_wii": 90, "wpi": 10}
	nullQuality := map[string]any{"global_wii": 90, "wpi": 10, "terminal_quality": nil}
	for _, test := range []struct {
		name       string
		arms       any
		weight     float64
		ingestDate string
		valid      bool
	}{
		{"complete with explicit zero", domainArmsForTest(), 0.15, "2026-09-28", true},
		{"changed weight", domainArmsForTest(), 0.9, "2026-09-28", false},
		{"missing effort arm", map[string]selection.DomainArmEvidence{"vendor-a/quality": domainArmsForTest()["vendor-a/quality"]}, 0.15, "2026-09-28", false},
		{"omitted terminal quality", map[string]any{"vendor-a/quality": omittedQuality, "vendor-b/cheap": domainArmsForTest()["vendor-b/cheap"]}, 0.15, "2026-09-28", false},
		{"null terminal quality", map[string]any{"vendor-a/quality": nullQuality, "vendor-b/cheap": domainArmsForTest()["vendor-b/cheap"]}, 0.15, "2026-09-28", false},
		{"impossible ingest date", domainArmsForTest(), 0.15, "2026-13-99", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := selection.ParseDomainEvidence(domainEvidencePayloadForTest(t, roster, test.arms, test.weight, test.ingestDate), roster)
			if test.valid {
				require.NoError(t, err)
				assert.Equal(t, 100.0, *parsed.Arms["vendor-b/cheap"].TerminalQuality)
			} else {
				require.Error(t, err, fmt.Sprintf("%s should fail closed", test.name))
			}
		})
	}
}

func TestSparseDomainInvalidEvidencePreservesBaseline(t *testing.T) {
	roster := dynamicRoster()
	groups := []selection.Group{{Label: "low"}}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	profile := fullProfile(selection.DomainInfra)
	for _, test := range []struct {
		name   string
		mutate func(*selection.DomainEvidence)
	}{
		{"missing exact arm", func(evidence *selection.DomainEvidence) { delete(evidence.Arms, "vendor-a/quality") }},
		{"stale roster", func(evidence *selection.DomainEvidence) { evidence.RosterSHA256 = strings.Repeat("f", 64) }},
		{"stale indices", func(evidence *selection.DomainEvidence) {
			cell := evidence.Arms["vendor-a/quality"]
			cell.GlobalWII = 0
			evidence.Arms["vendor-a/quality"] = cell
		}},
		{"missing terminal quality", func(evidence *selection.DomainEvidence) {
			cell := evidence.Arms["vendor-a/quality"]
			cell.TerminalQuality = nil
			evidence.Arms["vendor-a/quality"] = cell
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := domainEvidenceForTest(t, roster)
			test.mutate(evidence)
			pick, scores, _, order, ok := selection.SelectGroupsWithDomainPreferences(roster, groups, "", candidates, nil, nil, evidence, profile)
			require.True(t, ok)
			assert.Equal(t, "vendor-a/quality", pick.Arm)
			assert.Equal(t, float32(30), scores["low"]["vendor-a/quality"])
			assert.Equal(t, roster.Clusters["low"].Arms, order["low"])
		})
	}
}

func TestSparseDomainUnlistedScoredArmPreservesBaseline(t *testing.T) {
	qualityHeavy := 0.8
	for _, test := range []struct {
		name     string
		addToMap func(*rosterdata.Cluster)
		bias     *float64
	}{
		{"neutral score map", func(cluster *rosterdata.Cluster) { cluster.ArmScores["vendor-c/unlisted"] = 35 }, nil},
		{"dynamic index map", func(cluster *rosterdata.Cluster) {
			cluster.ArmIndices["vendor-c/unlisted"] = rosterdata.ArmIndices{WII: 100}
		}, &qualityHeavy},
	} {
		t.Run(test.name, func(t *testing.T) {
			roster := dynamicRoster()
			evidence := domainEvidenceForTest(t, roster)
			cluster := roster.Clusters["low"]
			test.addToMap(&cluster)
			roster.Clusters["low"] = cluster
			pick, scores, _, _, ok := selection.SelectGroupsWithDomainPreferences(
				roster, []selection.Group{{Label: "low"}}, "", candidateSet("vendor-a/quality", "vendor-b/cheap"),
				test.bias, nil, evidence, fullProfile(selection.DomainInfra),
			)
			require.True(t, ok)
			assert.Equal(t, "vendor-a/quality", pick.Arm)
			assert.Equal(t, selection.Scores(roster, "low", cluster, test.bias)["vendor-a/quality"], scores["low"]["vendor-a/quality"])
		})
	}
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func fullProfile(active selection.Domain) selection.DomainProfile {
	return selection.DomainProfile{
		selection.DomainUI:    active == selection.DomainUI,
		selection.DomainLogic: active == selection.DomainLogic,
		selection.DomainData:  active == selection.DomainData,
		selection.DomainInfra: active == selection.DomainInfra,
		selection.DomainDocs:  active == selection.DomainDocs,
	}
}

func popcount(mask int) int {
	count := 0
	for mask != 0 {
		count += mask & 1
		mask >>= 1
	}
	return count
}
