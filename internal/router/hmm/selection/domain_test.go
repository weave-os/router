package selection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
)

func domainArmsForTest() map[string]DomainArmEvidence {
	return map[string]DomainArmEvidence{
		"vendor-a/quality": {GlobalWII: 90, WPI: 10, Benchmarks: map[Benchmark]float64{
			BenchmarkTerminalBench4: 0, BenchmarkITBench: 0, BenchmarkLongContextReasoning: 90,
		}},
		"vendor-b/cheap": {GlobalWII: 55, WPI: 0, Benchmarks: map[Benchmark]float64{BenchmarkTerminalBench4: 100}},
	}
}

func domainRecipesForTest() map[string]map[string]float64 {
	return map[string]map[string]float64{
		"ui":    {"terminalbench_v4_0": 0.7, "lcr": 0.2, "ifbench": 0.1},
		"logic": {"terminalbench_v4_0": 0.7, "lcr": 0.2, "ifbench": 0.1},
		"data":  {"terminalbench_v4_0": 0.4, "lcr": 0.1, "analyst_agent": 0.3, "terminalbench_science": 0.2},
		"infra": {"terminalbench_v4_0": 0.6, "ifbench": 0.1, "itbench_sre": 0.3},
	}
}

func bindDomainRosterForTest(roster *rosterdata.Roster) {
	roster.SHA256 = strings.Repeat("a", 64)
	roster.Ranking.WIIScoreVersion = "authored-wii"
	roster.Ranking.WIINormalizationSHA256 = strings.Repeat("b", 64)
	roster.Ranking.WPIScoreVersion = "authored-wpi"
	roster.Ranking.WPINormalizationSHA256 = strings.Repeat("c", 64)
}

type domainEvidenceFixture struct {
	arms             any
	ingestDate       string
	additionalFields map[string]any
}

func validDomainEvidenceFixture() domainEvidenceFixture {
	return domainEvidenceFixture{arms: domainArmsForTest(), ingestDate: "2026-10-05"}
}

func domainEvidencePayloadForTest(t *testing.T, roster *rosterdata.Roster, fixture domainEvidenceFixture) []byte {
	t.Helper()
	document := map[string]any{
		"schema_version": "domain_wmi_evidence_v3", "source_snapshot_sha256": strings.Repeat("d", 64), "source_ingest_date": "2026-10-02",
		"benchmark_snapshot_sha256": strings.Repeat("e", 64), "benchmark_ingest_date": fixture.ingestDate,
		"roster_sha256":     roster.SHA256,
		"wii_score_version": "authored-wii", "wii_normalization_sha256": roster.Ranking.WIINormalizationSHA256,
		"wpi_score_version": "authored-wpi", "wpi_normalization_sha256": roster.Ranking.WPINormalizationSHA256,
		"arms": fixture.arms,
	}
	for field, value := range fixture.additionalFields {
		document[field] = value
	}
	payload, err := json.Marshal(document)
	require.NoError(t, err)
	return payload
}

func domainEvidenceForTest(t *testing.T, roster *rosterdata.Roster) *DomainEvidence {
	t.Helper()
	bindDomainRosterForTest(roster)
	evidence, err := ParseDomainEvidence(domainEvidencePayloadForTest(t, roster, validDomainEvidenceFixture()), roster)
	require.NoError(t, err)
	return evidence
}

// expectedTaskDelta restates the recipe independently of the selection package.
func expectedTaskDelta(profile DomainProfile, arm DomainArmEvidence) float64 {
	total, active := 0.0, 0
	for domain, weights := range domainRecipesForTest() {
		if !profile[Domain(domain)] {
			continue
		}
		active++
		for benchmark, weight := range weights {
			if quality, measured := arm.Benchmarks[Benchmark(benchmark)]; measured {
				total += weight * (quality - arm.GlobalWII)
			}
		}
	}
	if active == 0 {
		return 0
	}
	return total / float64(active)
}

func TestDomainScoresFollowWeightedRecipeForAllMasks(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	groups := []rosterdata.Group{"low"}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	for mask := 0; mask < 32; mask++ {
		profile := DomainProfile{}
		for bit, domain := range []Domain{DomainUI, DomainLogic, DomainData, DomainInfra, DomainDocs} {
			profile[domain] = mask&(1<<bit) != 0
		}
		pick, scores, components, order, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, LadderKey: SubscriptionLadder("", nil), Candidates: candidates, Evidence: evidence, Profile: profile}).legacyResults()
		require.True(t, ok)
		assert.ElementsMatch(t, roster.Clusters["low"].Arms, order["low"])
		for arm, base := range map[string]float64{"vendor-a/quality": 30, "vendor-b/cheap": 25} {
			correction := 0.4 * 0.15 * expectedTaskDelta(profile, evidence.Arms[arm])
			assert.InDelta(t, base+correction, scores["low"][arm], 1e-5, "mask %d arm %s", mask, arm)
			assert.InDelta(t, correction, components["low"][arm].TaskDomainCorrection, 1e-5)
		}
		if len(ScoredDomains(profile)) == 0 {
			assert.Equal(t, "vendor-a/quality", pick.Arm.String())
			assert.Equal(t, roster.Clusters["low"].Arms, order["low"])
		}
	}
}

func TestDocsNeitherCorrectsNorDilutes(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	arm := evidence.Arms["vendor-a/quality"]
	logicOnly := fullProfile(DomainLogic)
	logicAndDocs := fullProfile(DomainLogic)
	logicAndDocs[DomainDocs] = true
	assert.Equal(t, []Domain{DomainLogic}, ScoredDomains(logicAndDocs))
	assert.InDelta(t, TaskQualityDelta(ScoredDomains(logicOnly), arm), TaskQualityDelta(ScoredDomains(logicAndDocs), arm), 1e-9)
	assert.Empty(t, ScoredDomains(fullProfile(DomainDocs)))
	assert.Empty(t, ScoredDomains(nil))
	assert.Empty(t, ScoredDomains(DomainProfile{DomainInfra: true}))
	assert.Empty(t, ScoredDomains(DomainProfile{
		DomainUI: false, DomainLogic: false, DomainData: false,
		DomainInfra: true, Domain("unexpected"): false,
	}))
}

func TestDomainMultipleActionsAverageRecipes(t *testing.T) {
	arm := DomainArmEvidence{GlobalWII: 50, Benchmarks: map[Benchmark]float64{
		BenchmarkTerminalBench4: 70, BenchmarkAnalystAgent: 20, BenchmarkITBench: 80,
	}}
	data := 0.4*20 + 0.3*-30
	infra := 0.6*20 + 0.3*30
	assert.InDelta(t, data, TaskQualityDelta([]Domain{DomainData}, arm), 1e-12)
	assert.InDelta(t, infra, TaskQualityDelta([]Domain{DomainInfra}, arm), 1e-12)
	assert.InDelta(t, (data+infra)/2, TaskQualityDelta([]Domain{DomainData, DomainInfra}, arm), 1e-12)
}

func TestDomainArmWithoutRelevantScoresKeepsItsScore(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	cell := evidence.Arms["vendor-b/cheap"]
	cell.Benchmarks = map[Benchmark]float64{BenchmarkAnalystAgent: 100}
	evidence.Arms["vendor-b/cheap"] = cell
	_, scores, components, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"low"}, LadderKey: SubscriptionLadder("", nil), Candidates: candidateSet("vendor-a/quality", "vendor-b/cheap"), Evidence: evidence, Profile: fullProfile(DomainLogic)}).legacyResults()
	require.True(t, ok)
	assert.InDelta(t, float32(25), scores["low"]["vendor-b/cheap"], 1e-9)
	assert.Zero(t, components["low"]["vendor-b/cheap"].TaskDomainCorrection)
	assert.Less(t, scores["low"]["vendor-a/quality"], float32(30))
}

func TestDomainWithoutEvidencePreservesExistingSelection(t *testing.T) {
	roster := dynamicRoster()
	groups := []rosterdata.Group{"low"}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	qualityBias := 0.8
	baselinePick, baselineScores, baselineComponents, baselineOrder, baselineOK := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, LadderKey: SubscriptionLadder("", nil), Candidates: candidates, QualityBias: &qualityBias, PreferredModels: []string{"vendor-b/cheap"}}).legacyResults()
	for _, profile := range []DomainProfile{nil, fullProfile(DomainInfra)} {
		pick, scores, components, order, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, LadderKey: SubscriptionLadder("", nil), Candidates: candidates, QualityBias: &qualityBias, PreferredModels: []string{"vendor-b/cheap"}, Profile: profile}).legacyResults()
		assert.Equal(t, baselineOK, ok)
		assert.Equal(t, baselinePick, pick)
		assert.Equal(t, baselineScores, scores)
		assert.Equal(t, baselineComponents, components)
		assert.Equal(t, baselineOrder, order)
	}
}

func TestDomainRetainsPinVendorAndEligibility(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	cluster := roster.Clusters["low"]
	cluster.ManualPinsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessPI: {"vendor-a/quality"}}
	cluster.PreferredVendorsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessCodex: {"vendor-a"}}
	roster.Clusters["low"] = cluster
	profile := fullProfile(DomainInfra)
	groups := []rosterdata.Group{"low"}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, LadderKey: SubscriptionLadder("", nil), Candidates: candidates, Evidence: evidence, Profile: profile}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-b/cheap", pick.Arm.String())
	pick, _, _, _, ok = selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, Harness: "pi", LadderKey: SubscriptionLadder("pi", nil), Candidates: candidates, Evidence: evidence, Profile: profile}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm.String())
	pick, _, _, _, ok = selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, Harness: "codex", LadderKey: SubscriptionLadder("codex", nil), Candidates: candidates, Evidence: evidence, Profile: profile}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm.String())
	pick, _, _, _, ok = selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, LadderKey: SubscriptionLadder("", nil), Candidates: candidateSet("vendor-a/quality"), Evidence: evidence, Profile: profile}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm.String())
}

func TestDomainZeroCorrectionKeepsNeutralOrder(t *testing.T) {
	roster := dynamicRoster()
	cluster := roster.Clusters["low"]
	cluster.ManualPinsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessPI: {"vendor-b/cheap"}}
	roster.Clusters["low"] = cluster
	evidence := domainEvidenceForTest(t, roster)
	for arm, cell := range evidence.Arms {
		for benchmark := range cell.Benchmarks {
			cell.Benchmarks[benchmark] = cell.GlobalWII
		}
		evidence.Arms[arm] = cell
	}
	pick, scores, _, orders, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"low"}, Harness: "pi", LadderKey: SubscriptionLadder("pi", nil), Candidates: candidateSet("vendor-a/quality", "vendor-b/cheap"), Evidence: evidence, Profile: fullProfile(DomainInfra)}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm.String())
	assert.Equal(t, cluster.Arms, orders["low"])
	assert.InDelta(t, float32(30), scores["low"]["vendor-a/quality"], 1e-9)
}

func TestDomainKeepsUserQualityPreference(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	profile := fullProfile(DomainLogic)
	priceHeavy := 0.0
	_, scores, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"low"}, LadderKey: SubscriptionLadder("", nil), Candidates: candidateSet("vendor-a/quality", "vendor-b/cheap"), QualityBias: &priceHeavy, Evidence: evidence, Profile: profile}).legacyResults()
	require.True(t, ok)
	alpha := roster.Ranking.AlphaMin["low"]
	assert.InDelta(t, alpha*90-(1-alpha)*10+alpha*0.15*(0.7*(0-90)), scores["low"]["vendor-a/quality"], 1e-5)
	assert.InDelta(t, alpha*55+alpha*0.15*(0.7*(100-55)), scores["low"]["vendor-b/cheap"], 1e-5)
}

func TestDomainEvidenceRejectsRecipeDriftAndInvalidArms(t *testing.T) {
	roster := dynamicRoster()
	bindDomainRosterForTest(roster)
	withArms := func(arms any) domainEvidenceFixture {
		fixture := validDomainEvidenceFixture()
		fixture.arms = arms
		return fixture
	}
	unscored := map[string]any{"global_wii": 55, "wpi": 0}
	for _, test := range []struct {
		name    string
		fixture domainEvidenceFixture
		valid   bool
	}{
		{"complete", validDomainEvidenceFixture(), true},
		{"arm without benchmarks", withArms(map[string]any{"vendor-a/quality": domainArmsForTest()["vendor-a/quality"], "vendor-b/cheap": unscored}), true},
		{"producer-supplied recipe", func() domainEvidenceFixture {
			fixture := validDomainEvidenceFixture()
			fixture.additionalFields = map[string]any{"recipes": domainRecipesForTest()}
			return fixture
		}(), false},
		{"missing effort arm", withArms(map[string]DomainArmEvidence{"vendor-a/quality": domainArmsForTest()["vendor-a/quality"]}), false},
		{"unknown benchmark", withArms(map[string]any{"vendor-a/quality": domainArmsForTest()["vendor-a/quality"], "vendor-b/cheap": map[string]any{"global_wii": 55, "wpi": 0, "benchmarks": map[string]float64{"terminalbench_v2_1": 50}}}), false},
		{"null quality", withArms(map[string]any{"vendor-a/quality": domainArmsForTest()["vendor-a/quality"], "vendor-b/cheap": map[string]any{"global_wii": 55, "wpi": 0, "benchmarks": map[string]any{"terminalbench_v4_0": nil}}}), false},
		{"null arm", withArms(map[string]any{"vendor-a/quality": domainArmsForTest()["vendor-a/quality"], "vendor-b/cheap": nil}), false},
		{"null global wii", withArms(map[string]any{"vendor-a/quality": domainArmsForTest()["vendor-a/quality"], "vendor-b/cheap": map[string]any{"global_wii": nil, "wpi": 0, "benchmarks": map[string]any{}}}), false},
		{"missing wpi", withArms(map[string]any{"vendor-a/quality": domainArmsForTest()["vendor-a/quality"], "vendor-b/cheap": map[string]any{"global_wii": 55, "benchmarks": map[string]any{}}}), false},
		{"out-of-range quality", withArms(map[string]any{"vendor-a/quality": domainArmsForTest()["vendor-a/quality"], "vendor-b/cheap": map[string]any{"global_wii": 55, "wpi": 0, "benchmarks": map[string]float64{"terminalbench_v4_0": 101}}}), false},
		{"impossible ingest date", func() domainEvidenceFixture {
			fixture := validDomainEvidenceFixture()
			fixture.ingestDate = "2026-13-99"
			return fixture
		}(), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseDomainEvidence(domainEvidencePayloadForTest(t, roster, test.fixture), roster)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "%s should fail closed", test.name)
			}
		})
	}
}

func TestDomainInvalidEvidencePreservesBaseline(t *testing.T) {
	roster := dynamicRoster()
	groups := []rosterdata.Group{"low"}
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	profile := fullProfile(DomainInfra)
	for _, test := range []struct {
		name   string
		mutate func(*DomainEvidence)
	}{
		{"missing exact arm", func(evidence *DomainEvidence) { delete(evidence.Arms, "vendor-a/quality") }},
		{"stale roster", func(evidence *DomainEvidence) { evidence.RosterSHA256 = strings.Repeat("f", 64) }},
		{"stale indices", func(evidence *DomainEvidence) {
			cell := evidence.Arms["vendor-a/quality"]
			cell.GlobalWII = 0
			evidence.Arms["vendor-a/quality"] = cell
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := domainEvidenceForTest(t, roster)
			test.mutate(evidence)
			pick, scores, _, order, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, LadderKey: SubscriptionLadder("", nil), Candidates: candidates, Evidence: evidence, Profile: profile}).legacyResults()
			require.True(t, ok)
			assert.Equal(t, "vendor-a/quality", pick.Arm.String())
			assert.InDelta(t, float32(30), scores["low"]["vendor-a/quality"], 1e-9)
			assert.Equal(t, roster.Clusters["low"].Arms, order["low"])
		})
	}
}

func TestDomainUnlistedScoredArmPreservesBaseline(t *testing.T) {
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
			pick, scores, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"low"}, LadderKey: SubscriptionLadder("", nil), Candidates: candidateSet("vendor-a/quality", "vendor-b/cheap"), QualityBias: test.bias, Evidence: evidence, Profile: fullProfile(DomainInfra)}).legacyResults()
			require.True(t, ok)
			assert.Equal(t, "vendor-a/quality", pick.Arm.String())
			assert.InDelta(t, Scores(roster, "low", cluster, test.bias)["vendor-a/quality"], scores["low"]["vendor-a/quality"], 1e-9)
		})
	}
}

func fullProfile(active Domain) DomainProfile {
	return DomainProfile{
		DomainUI:    active == DomainUI,
		DomainLogic: active == DomainLogic,
		DomainData:  active == DomainData,
		DomainInfra: active == DomainInfra,
		DomainDocs:  active == DomainDocs,
	}
}
