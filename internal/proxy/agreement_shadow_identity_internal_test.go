package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/switchpolicy"
)

func TestAgreementShadowStrategyLabelsPreserveVotes(t *testing.T) {
	for _, strategies := range [][2]router.Strategy{
		{router.StrategyHMM, router.StrategyHMMEmbedding},
		{router.StrategyHMMEmbedding, router.StrategyHMM},
	} {
		t.Run(string(strategies[0])+"_to_"+string(strategies[1]), func(t *testing.T) {
			ctx := agreementContext(policyregistry.TargetInternal, true)
			telemetry, turn := agreementFixture()
			turn.Fresh.Metadata.Strategy = string(strategies[0])
			applyAgreementShadowTelemetry(ctx, &telemetry, turn)
			require.NotNil(t, telemetry.agreementShadow)
			snapshot, first, err := switchpolicy.EvaluateShadow(switchpolicy.ShadowSnapshot{}, false, telemetry.agreementShadow.Observation)
			require.NoError(t, err)
			require.False(t, first.TwoUp.Switched)
			require.Equal(t, 1, first.TwoUp.Votes)

			telemetry.RequestID = "second-synthetic-request"
			telemetry.Timestamp = telemetry.Timestamp.Add(time.Second)
			turn.Fresh.Metadata.Strategy = string(strategies[1])
			applyAgreementShadowTelemetry(ctx, &telemetry, turn)
			_, second, err := switchpolicy.EvaluateShadow(snapshot, true, telemetry.agreementShadow.Observation)
			require.NoError(t, err)
			require.Equal(t, switchpolicy.ShadowGapNone, second.Gap)
			require.Equal(t, first.SegmentID, second.SegmentID)
			require.False(t, second.StartedSegment)
			require.True(t, second.TwoUp.Switched)
			require.Equal(t, 2, second.TwoUp.Votes)
			require.Equal(t, agreementSol, second.TwoUp.Next.Current.Target.Model)
		})
	}
}

func TestAgreementShadowPolicyChangesStillResetVotes(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*router.RoutingMetadata)
	}{
		{"classifier", func(md *router.RoutingMetadata) { md.ClassifierArtifactSHA256 = "other-synthetic-classifier" }},
		{"selection policy", func(md *router.RoutingMetadata) { md.SelectionPolicySHA256 = "other-synthetic-policy" }},
		{"roster", func(md *router.RoutingMetadata) { md.RosterVersion = "other-synthetic-roster" }},
		{"beta strategy", func(md *router.RoutingMetadata) { md.Strategy = string(router.StrategyHMMBeta) }},
		{"qwen strategy", func(md *router.RoutingMetadata) { md.Strategy = string(router.StrategyHMMQwen) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := agreementContext(policyregistry.TargetInternal, true)
			telemetry, turn := agreementFixture()
			applyAgreementShadowTelemetry(ctx, &telemetry, turn)
			snapshot, first, err := switchpolicy.EvaluateShadow(switchpolicy.ShadowSnapshot{}, false, telemetry.agreementShadow.Observation)
			require.NoError(t, err)
			require.Equal(t, 1, first.TwoUp.Votes)

			telemetry.RequestID = "changed-synthetic-request"
			telemetry.Timestamp = telemetry.Timestamp.Add(time.Second)
			test.change(turn.Fresh.Metadata)
			applyAgreementShadowTelemetry(ctx, &telemetry, turn)
			_, second, err := switchpolicy.EvaluateShadow(snapshot, true, telemetry.agreementShadow.Observation)
			require.NoError(t, err)
			require.Equal(t, switchpolicy.ShadowGapPolicyChanged, second.Gap)
			require.Equal(t, first.SegmentID, second.InvalidatedSegment)
			require.True(t, second.StartedSegment)
			require.False(t, second.TwoUp.Switched)
			require.Equal(t, 1, second.TwoUp.Votes)
		})
	}
}
