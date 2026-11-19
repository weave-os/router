package selectionpolicytest

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"
)

const (
	defaultAlpha       = 0.5
	defaultAlphaMin    = 0.1
	defaultAlphaMax    = 0.9
	defaultNeutralBias = 0.7
	defaultArmScore    = 1
	defaultArmIndex    = 50
)

func Admit(tb testing.TB, roster rosterdata.Roster) *selectionpolicy.Policy {
	tb.Helper()
	completeAsPolicyV1(&roster)
	payload, err := rosterdata.CanonicalBytes(&roster)
	require.NoError(tb, err)
	admitted, err := selectionpolicy.Admit(payload)
	require.NoError(tb, err)
	return admitted
}

func completeAsPolicyV1(roster *rosterdata.Roster) {
	roster.SchemaVersion = rosterdata.SchemaVersionPolicyV1
	roster.SHA256 = ""
	if len(roster.ClassOrder) == 0 {
		for label := range roster.Clusters {
			roster.ClassOrder = append(roster.ClassOrder, label)
		}
		sort.Strings(roster.ClassOrder)
	}
	ranking := &roster.Ranking
	ranking.Alpha = withDefault(ranking.Alpha, roster.Clusters, defaultAlpha)
	ranking.AlphaMin = withDefault(ranking.AlphaMin, roster.Clusters, defaultAlphaMin)
	ranking.AlphaMax = withDefault(ranking.AlphaMax, roster.Clusters, defaultAlphaMax)
	if ranking.QualityBiasNeutral == 0 {
		ranking.QualityBiasNeutral = defaultNeutralBias
	}
	ranking.WIIScoreVersion = nonEmpty(ranking.WIIScoreVersion, "wii-v1")
	ranking.WIINormalizationSHA256 = nonEmpty(ranking.WIINormalizationSHA256, "wii")
	ranking.WPIScoreVersion = nonEmpty(ranking.WPIScoreVersion, "wpi-v1")
	ranking.WPINormalizationSHA256 = nonEmpty(ranking.WPINormalizationSHA256, "wpi")
	for label, cluster := range roster.Clusters {
		cluster.ComplexityLabel = label
		if cluster.CostRefUSD == 0 {
			cluster.CostRefUSD = 1
		}
		if cluster.LatencyRefMS == 0 {
			cluster.LatencyRefMS = 1
		}
		if cluster.ArmScores == nil {
			cluster.ArmScores = make(map[string]float64)
		}
		if cluster.ArmIndices == nil {
			cluster.ArmIndices = make(map[string]rosterdata.ArmIndices)
		}
		arms := append([]string(nil), cluster.Arms...)
		for _, harnessArms := range cluster.ArmsByHarness {
			arms = append(arms, harnessArms...)
		}
		for _, arm := range arms {
			if _, scored := cluster.ArmScores[arm]; !scored {
				cluster.ArmScores[arm] = defaultArmScore
			}
			if _, indexed := cluster.ArmIndices[arm]; !indexed {
				cluster.ArmIndices[arm] = rosterdata.ArmIndices{WII: defaultArmIndex, WPI: defaultArmIndex}
			}
		}
		roster.Clusters[label] = cluster
	}
}

func withDefault(values map[string]float64, clusters map[string]rosterdata.Cluster, fallback float64) map[string]float64 {
	if values == nil {
		values = make(map[string]float64, len(clusters))
	}
	for label := range clusters {
		if _, set := values[label]; !set {
			values[label] = fallback
		}
	}
	return values
}

func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
