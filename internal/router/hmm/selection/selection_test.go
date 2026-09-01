package selection_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/router/hmm/rosterdata"
	"workweave/router/internal/router/hmm/selection"
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
		SchemaVersion: "hmm_router_cluster_roster_v6",
		Clusters: map[string]rosterdata.Cluster{
			"low": {
				Arms: []string{"vendor-a/cheap", "vendor-b/cheap"},
				ArmsByHarness: map[string][]string{
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
		},
	}
}

func TestArmOrder(t *testing.T) {
	roster := testRoster()

	order, harnessSpecific := selection.ArmOrder(roster.Clusters["low"], "claude-code")
	assert.True(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-b/cheap", "vendor-a/cheap"}, order)

	order, harnessSpecific = selection.ArmOrder(roster.Clusters["low"], "codex")
	assert.False(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-a/cheap", "vendor-b/cheap"}, order)

	// Roster harness keys use underscores while ClientApp is hyphenated.
	order, harnessSpecific = selection.ArmOrder(roster.Clusters["low"], "codex-cli")
	assert.True(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-b/cheap"}, order)

	order, harnessSpecific = selection.ArmOrder(roster.Clusters["balanced"], "claude-code")
	assert.False(t, harnessSpecific)
	assert.Equal(t, []string{"vendor-a/mid", "vendor-b/mid", "vendor-a/cheap"}, order)
}

func TestSelect(t *testing.T) {
	roster := testRoster()

	tests := []struct {
		name         string
		rankedGroups []string
		harness      string
		candidates   map[string]struct{}
		wantOK       bool
		want         selection.Pick
	}{
		{
			name:         "rank one arm of the top group wins",
			rankedGroups: []string{"balanced", "low", "high"},
			candidates:   candidateSet("vendor-b/mid", "vendor-a/mid", "vendor-a/cheap"),
			wantOK:       true,
			want:         selection.Pick{Group: "balanced", Arm: "vendor-a/mid"},
		},
		{
			name:         "lower-ranked roster arm loses to roster order not candidate order",
			rankedGroups: []string{"balanced"},
			candidates:   candidateSet("vendor-a/cheap", "vendor-b/mid"),
			wantOK:       true,
			want:         selection.Pick{Group: "balanced", Arm: "vendor-b/mid"},
		},
		{
			name:         "empty intersection falls back to the next ranked group",
			rankedGroups: []string{"high", "balanced", "low"},
			candidates:   candidateSet("vendor-a/cheap"),
			wantOK:       true,
			want:         selection.Pick{Group: "balanced", Arm: "vendor-a/cheap", FallbackDepth: 1},
		},
		{
			name:         "fallback walks every ranked group in order",
			rankedGroups: []string{"high", "balanced", "low"},
			candidates:   candidateSet("vendor-b/cheap"),
			wantOK:       true,
			want:         selection.Pick{Group: "low", Arm: "vendor-b/cheap", FallbackDepth: 2},
		},
		{
			name:         "harness-specific order flips the pick",
			rankedGroups: []string{"low"},
			harness:      "claude-code",
			candidates:   candidateSet("vendor-a/cheap", "vendor-b/cheap"),
			wantOK:       true,
			want:         selection.Pick{Group: "low", Arm: "vendor-b/cheap", HarnessOrder: true},
		},
		{
			name:         "unknown harness uses the pooled order",
			rankedGroups: []string{"low"},
			harness:      "codex",
			candidates:   candidateSet("vendor-a/cheap", "vendor-b/cheap"),
			wantOK:       true,
			want:         selection.Pick{Group: "low", Arm: "vendor-a/cheap"},
		},
		{
			name:         "hyphenated harness matches an underscore roster key",
			rankedGroups: []string{"low"},
			harness:      "codex-cli",
			candidates:   candidateSet("vendor-a/cheap", "vendor-b/cheap"),
			wantOK:       true,
			want:         selection.Pick{Group: "low", Arm: "vendor-b/cheap", HarnessOrder: true},
		},
		{
			name:         "effort-suffixed arm matches its base candidate roster ID",
			rankedGroups: []string{"effort"},
			candidates:   candidateSet("vendor-a/deep"),
			wantOK:       true,
			want:         selection.Pick{Group: "effort", Arm: "vendor-a/deep:high"},
		},
		{
			name:         "ranked label missing from the roster is walked past",
			rankedGroups: []string{"retired", "low"},
			candidates:   candidateSet("vendor-a/cheap"),
			wantOK:       true,
			want:         selection.Pick{Group: "low", Arm: "vendor-a/cheap", FallbackDepth: 1},
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
			pick, ok := selection.Select(roster, tc.rankedGroups, tc.harness, tc.candidates)
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, pick)
			}
		})
	}
}

func TestSelectGroupsHonorsTheSidecarArmAllowlist(t *testing.T) {
	roster := testRoster()
	candidates := candidateSet("vendor-a/mid", "vendor-b/mid", "vendor-a/cheap")

	// Rank one of the group is a candidate but the sidecar excluded it (e.g. a
	// capability constraint), so the next allowed arm of the same group serves.
	pick, ok := selection.SelectGroups(
		roster,
		[]selection.Group{{Label: "balanced", AllowedArms: []string{"vendor-b/mid"}}},
		"",
		candidates,
	)
	require.True(t, ok)
	assert.Equal(t, selection.Pick{Group: "balanced", Arm: "vendor-b/mid"}, pick)

	// A group whose allowlist excludes every candidate falls through.
	pick, ok = selection.SelectGroups(
		roster,
		[]selection.Group{
			{Label: "balanced", AllowedArms: []string{"vendor-c/other"}},
			{Label: "low", AllowedArms: []string{"vendor-a/cheap"}},
		},
		"",
		candidates,
	)
	require.True(t, ok)
	assert.Equal(t, selection.Pick{Group: "low", Arm: "vendor-a/cheap", FallbackDepth: 1}, pick)

	// An empty allowlist is "no restriction", not "no arms": a sidecar roster
	// that disagrees with the router's must not shrink the candidate set.
	pick, ok = selection.SelectGroups(
		roster,
		[]selection.Group{{Label: "balanced"}},
		"",
		candidates,
	)
	require.True(t, ok)
	assert.Equal(t, selection.Pick{Group: "balanced", Arm: "vendor-a/mid"}, pick)

	// Allowlist entries match effort-suffixed roster arms on their base ID.
	pick, ok = selection.SelectGroups(
		roster,
		[]selection.Group{{Label: "effort", AllowedArms: []string{"vendor-a/deep"}}},
		"",
		candidateSet("vendor-a/deep"),
	)
	require.True(t, ok)
	assert.Equal(t, selection.Pick{Group: "effort", Arm: "vendor-a/deep:high"}, pick)
}

func TestSelectIsDeterministic(t *testing.T) {
	roster := testRoster()
	candidates := candidateSet("vendor-a/mid", "vendor-b/mid", "vendor-a/cheap", "vendor-b/cheap")
	first, ok := selection.Select(roster, []string{"balanced", "low"}, "", candidates)
	require.True(t, ok)
	for range 100 {
		pick, ok := selection.Select(roster, []string{"balanced", "low"}, "", candidates)
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
			harness:      "claude-code",
			candidates:   []string{"vendor-a/cheap", "vendor-b/cheap"},
			wantGroup:    "low",
			wantArm:      "vendor-b/cheap",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pick, ok := selection.Select(roster, tc.rankedGroups, tc.harness, candidateSet(tc.candidates...))
			require.True(t, ok)
			assert.Equal(t, tc.wantGroup, pick.Group)
			assert.Equal(t, tc.wantArm, pick.Arm)
		})
	}
}
