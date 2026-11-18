package selection

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

func candidateSet(ids ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func testRoster() *rosterdata.Roster {
	return &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		Clusters: map[string]rosterdata.Cluster{
			"low": {
				Arms: []string{"vendor-a/cheap", "vendor-b/cheap"},
				ArmsByHarness: map[rosterdata.Harness][]string{
					"claude-code": {"vendor-b/cheap", "vendor-a/cheap"},
					"codex_cli":   {"vendor-b/cheap"},
				},
			},
			"balanced": {
				Arms: []string{"vendor-a/mid", "vendor-b/mid", "vendor-a/cheap"},
			},
			"high": {
				Arms: []string{"vendor-a/top", "vendor-b/top"},
			},
			"effort": {
				Arms: []string{"vendor-a/deep:high", "vendor-a/deep"},
			},
			"efforts": {
				Arms: []string{"vendor-a/deep:xhigh", "vendor-a/deep:low", "vendor-a/deep"},
			},
		},
	}
}

func dynamicRoster() *rosterdata.Roster {
	return &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionV7,
		Ranking: rosterdata.Ranking{
			Alpha:              map[string]float64{"low": 0.4},
			AlphaMin:           map[string]float64{"low": 0.05},
			AlphaMax:           map[string]float64{"low": 0.8},
			QualityBiasNeutral: 0.7,
		},
		Clusters: map[string]rosterdata.Cluster{
			"low": {
				Arms: []string{"vendor-a/quality", "vendor-b/cheap"},
				ArmScores: map[string]float64{
					"vendor-a/quality": 30,
					"vendor-b/cheap":   25,
				},
				ArmIndices: map[string]rosterdata.ArmIndices{
					"vendor-a/quality": {WII: 90, WPI: 10},
					"vendor-b/cheap":   {WII: 55, WPI: 0},
				},
			},
		},
	}
}

func TestSelectGroupsWithPreferenceReweightsWithinClassifierBand(t *testing.T) {
	roster := dynamicRoster()
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")
	groups := []rosterdata.Group{"low"}

	neutral := 0.7
	pick, scores, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, LadderKey: SubscriptionLadder("", nil), Candidates: candidates, QualityBias: &neutral}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm.String())
	assert.InDelta(t, float32(30), scores["low"]["vendor-a/quality"], 1e-9)

	priceHeavy := 0.0
	pick, _, _, _, ok = selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, LadderKey: SubscriptionLadder("", nil), Candidates: candidates, QualityBias: &priceHeavy}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-b/cheap", pick.Arm.String())

	qualityHeavy := 1.0
	pick, _, _, _, ok = selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: groups, LadderKey: SubscriptionLadder("", nil), Candidates: candidates, QualityBias: &qualityHeavy}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-a/quality", pick.Arm.String())
}

func TestSelectGroupsWithPreferencePreservesPolicyTiers(t *testing.T) {
	roster := dynamicRoster()
	cluster := roster.Clusters["low"]
	cluster.ManualPinsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessPI: {"vendor-b/cheap"}}
	cluster.PreferredVendorsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessCodex: {"vendor-b"}}
	roster.Clusters["low"] = cluster
	qualityHeavy := 1.0
	candidates := candidateSet("vendor-a/quality", "vendor-b/cheap")

	pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"low"}, Harness: "pi", LadderKey: SubscriptionLadder("pi", nil), Candidates: candidates, QualityBias: &qualityHeavy}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-b/cheap", pick.Arm.String(), "manual pin must outrank the dynamic score")

	pick, _, _, _, ok = selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"low"}, Harness: "codex", LadderKey: SubscriptionLadder("codex", nil), Candidates: candidates, QualityBias: &qualityHeavy}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, "vendor-b/cheap", pick.Arm.String(), "vendor affinity must outrank the dynamic score")
}

func TestSelectGroupsWithPreferenceKeepsFallbackOrder(t *testing.T) {
	roster := dynamicRoster()
	high := roster.Clusters["low"]
	high.Arms = []string{"vendor-c/high"}
	high.ArmScores = map[string]float64{"vendor-c/high": 80}
	high.ArmIndices = map[string]rosterdata.ArmIndices{"vendor-c/high": {WII: 100, WPI: 100}}
	roster.Clusters["high"] = high
	roster.Ranking.Alpha["high"] = 0.85
	roster.Ranking.AlphaMin["high"] = 0.6
	roster.Ranking.AlphaMax["high"] = 0.98
	qualityHeavy := 1.0

	pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"low", "high"}, LadderKey: SubscriptionLadder("", nil), Candidates: candidateSet("vendor-a/quality", "vendor-c/high"), QualityBias: &qualityHeavy}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, rosterdata.Group("low"), pick.Group)
}

func TestArmOrder(t *testing.T) {
	roster := testRoster()

	order, harnessSpecific := ArmOrder(roster.Clusters["low"], "claude-code")
	assert.True(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-b/cheap", "vendor-a/cheap"}, order)

	order, harnessSpecific = ArmOrder(roster.Clusters["low"], "codex")
	assert.False(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-a/cheap", "vendor-b/cheap"}, order)

	// Roster harness keys use underscores while ClientApp is hyphenated.
	order, harnessSpecific = ArmOrder(roster.Clusters["low"], "codex-cli")
	assert.True(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-b/cheap"}, order)

	order, harnessSpecific = ArmOrder(roster.Clusters["balanced"], "claude-code")
	assert.False(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-a/mid", "vendor-b/mid", "vendor-a/cheap"}, order)
}

// OpenCode is its own policy identity: selection keys the roster on the
// request's client_app, so the roster harness must spell it identically to
// the sidecar vocabulary. Without a declared order (every promoted policy
// today) OpenCode serves the pooled cluster order.
func TestArmOrderOpenCodeHarness(t *testing.T) {
	require.Equal(t, policy.HarnessOpenCode, string(rosterdata.HarnessOpenCode))

	roster := testRoster()
	order, harnessSpecific := ArmOrder(roster.Clusters["low"], policy.HarnessOpenCode)
	assert.False(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-a/cheap", "vendor-b/cheap"}, order)

	low := roster.Clusters["low"]
	low.ArmsByHarness[rosterdata.HarnessOpenCode] = []string{"vendor-b/cheap", "vendor-a/cheap"}
	order, harnessSpecific = ArmOrder(low, policy.HarnessOpenCode)
	assert.True(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-b/cheap", "vendor-a/cheap"}, order)

	pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: rankedGroups([]string{"low"}), Harness: policy.HarnessOpenCode, LadderKey: SubscriptionLadder(policy.HarnessOpenCode, nil), Candidates: candidateSet("vendor-a/cheap", "vendor-b/cheap")}).legacyResults()
	require.True(t, ok)
	assert.Equal(t, Pick{Group: "low", Arm: armid.Parse("vendor-b/cheap"), HarnessOrder: true}, pick)
}

func TestSelect(t *testing.T) {
	roster := testRoster()

	tests := []struct {
		name         string
		rankedGroups []string
		harness      string
		candidates   map[string]struct{}
		wantOK       bool
		want         Pick
	}{
		{
			name:         "rank one arm of the top group wins",
			rankedGroups: []string{"balanced", "low", "high"},
			candidates:   candidateSet("vendor-b/mid", "vendor-a/mid", "vendor-a/cheap"),
			wantOK:       true,
			want:         Pick{Group: "balanced", Arm: armid.Parse("vendor-a/mid")},
		},
		{
			name:         "lower-ranked roster arm loses to roster order not candidate order",
			rankedGroups: []string{"balanced"},
			candidates:   candidateSet("vendor-a/cheap", "vendor-b/mid"),
			wantOK:       true,
			want:         Pick{Group: "balanced", Arm: armid.Parse("vendor-b/mid")},
		},
		{
			name:         "empty intersection falls back to the next ranked group",
			rankedGroups: []string{"high", "balanced", "low"},
			candidates:   candidateSet("vendor-a/cheap"),
			wantOK:       true,
			want:         Pick{Group: "balanced", Arm: armid.Parse("vendor-a/cheap"), FallbackDepth: 1},
		},
		{
			name:         "fallback walks every ranked group in order",
			rankedGroups: []string{"high", "balanced", "low"},
			candidates:   candidateSet("vendor-b/cheap"),
			wantOK:       true,
			want:         Pick{Group: "low", Arm: armid.Parse("vendor-b/cheap"), FallbackDepth: 2},
		},
		{
			name:         "harness-specific order flips the pick",
			rankedGroups: []string{"low"},
			harness:      "claude-code",
			candidates:   candidateSet("vendor-a/cheap", "vendor-b/cheap"),
			wantOK:       true,
			want:         Pick{Group: "low", Arm: armid.Parse("vendor-b/cheap"), HarnessOrder: true},
		},
		{
			name:         "unknown harness uses the pooled order",
			rankedGroups: []string{"low"},
			harness:      "codex",
			candidates:   candidateSet("vendor-a/cheap", "vendor-b/cheap"),
			wantOK:       true,
			want:         Pick{Group: "low", Arm: armid.Parse("vendor-a/cheap")},
		},
		{
			name:         "hyphenated harness matches an underscore roster key",
			rankedGroups: []string{"low"},
			harness:      "codex-cli",
			candidates:   candidateSet("vendor-a/cheap", "vendor-b/cheap"),
			wantOK:       true,
			want:         Pick{Group: "low", Arm: armid.Parse("vendor-b/cheap"), HarnessOrder: true},
		},
		{
			name:         "effort-suffixed arm matches its base candidate roster ID",
			rankedGroups: []string{"effort"},
			candidates:   candidateSet("vendor-a/deep"),
			wantOK:       true,
			want:         Pick{Group: "effort", Arm: armid.Parse("vendor-a/deep:high")},
		},
		{
			name:         "ranked label missing from the roster is walked past",
			rankedGroups: []string{"retired", "low"},
			candidates:   candidateSet("vendor-a/cheap"),
			wantOK:       true,
			want:         Pick{Group: "low", Arm: armid.Parse("vendor-a/cheap"), FallbackDepth: 1},
		},
		{
			name:         "no ranked group intersects the candidates",
			rankedGroups: []string{"high", "balanced", "low"},
			candidates:   candidateSet("vendor-c/other"),
			wantOK:       false,
		},
		{
			name:         "no ranked groups",
			rankedGroups: nil,
			candidates:   candidateSet("vendor-a/cheap"),
			wantOK:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: rankedGroups(tc.rankedGroups), Harness: tc.harness, LadderKey: SubscriptionLadder(tc.harness, nil), Candidates: tc.candidates}).legacyResults()
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, pick)
			}
		})
	}
}

func TestSelectIsDeterministic(t *testing.T) {
	roster := testRoster()
	candidates := candidateSet("vendor-a/mid", "vendor-b/mid", "vendor-a/cheap", "vendor-b/cheap")
	first, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: rankedGroups([]string{"balanced", "low"}), LadderKey: SubscriptionLadder("", nil), Candidates: candidates}).legacyResults()
	require.True(t, ok)
	for range 100 {
		pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: rankedGroups([]string{"balanced", "low"}), LadderKey: SubscriptionLadder("", nil), Candidates: candidates}).legacyResults()
		require.True(t, ok)
		require.Equal(t, first, pick)
	}
}

// TestSelectParityFixture pins expected picks against a fixture roster so
// loader/walk semantics drift shows up as a concrete pick change.
func TestSelectParityFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "parity_roster.json"))
	require.NoError(t, err)
	roster, err := rosterdata.Parse(data)
	require.NoError(t, err)

	tests := []struct {
		name         string
		rankedGroups []string
		harness      string
		candidates   []string
		wantGroup    string
		wantArm      string
	}{
		{
			name:         "full candidate set serves the top group's rank one",
			rankedGroups: []string{"balanced", "low", "high"},
			candidates:   []string{"vendor-a/mid", "vendor-b/mid", "vendor-a/cheap", "vendor-a/top"},
			wantGroup:    "balanced",
			wantArm:      "vendor-a/mid",
		},
		{
			name:         "rank one excluded serves rank two of the same group",
			rankedGroups: []string{"balanced", "low", "high"},
			candidates:   []string{"vendor-b/mid", "vendor-a/cheap", "vendor-a/top"},
			wantGroup:    "balanced",
			wantArm:      "vendor-b/mid",
		},
		{
			name:         "group exhausted falls through to the next ranked group",
			rankedGroups: []string{"balanced", "high", "low"},
			candidates:   []string{"vendor-a/top", "vendor-b/cheap"},
			wantGroup:    "high",
			wantArm:      "vendor-a/top",
		},
		{
			name:         "harness order overrides the pooled order",
			rankedGroups: []string{"low", "balanced", "high"},
			harness:      "claude_code",
			candidates:   []string{"vendor-a/cheap", "vendor-b/cheap"},
			wantGroup:    "low",
			wantArm:      "vendor-b/cheap",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: rankedGroups(tc.rankedGroups), Harness: tc.harness, LadderKey: SubscriptionLadder(tc.harness, nil), Candidates: candidateSet(tc.candidates...)}).legacyResults()
			require.True(t, ok)
			assert.Equal(t, rosterdata.Group(tc.wantGroup), pick.Group)
			assert.Equal(t, tc.wantArm, pick.Arm.String())
		})
	}
}

func rankedGroups(labels []string) []rosterdata.Group {
	groups := make([]rosterdata.Group, 0, len(labels))
	for _, label := range labels {
		groups = append(groups, rosterdata.Group(label))
	}
	return groups
}

func (s Selection) legacyResults() (Pick, map[string]map[string]float32, map[string]map[string]router.SelectionScoreComponents, map[string][]string, bool) {
	return s.Pick, s.ScoresByGroup, s.ComponentsByGroup, s.OrdersByGroup, s.Found
}
