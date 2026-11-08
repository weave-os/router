package policy_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

func TestSidecarRouterV5ArtifactMismatchFailsClosedAsPinError(t *testing.T) {
	releaseID := strings.Repeat("a", 64)
	selectionID := strings.Repeat("b", 64)
	taxonomySHA := strings.Repeat("c", 64)
	pinnedArtifactSHA := strings.Repeat("d", 64)
	servedArtifactSHA := strings.Repeat("e", 64)
	unavailable := errors.New("classifier unavailable")
	decider := &recordingPolicy{result: policy.Result{
		SchemaVersion: policy.SchemaVersionV5, Model: "claude-haiku-4-5", Provider: providers.ProviderAnthropic,
		PolicyArtifactID: "hmm-v5", PolicyArtifactSHA256: servedArtifactSHA,
		ClassifierReleaseID: releaseID, ClassifierSelectionID: selectionID,
		ClassifierVariantID: policy.ClassifierVariantPrimary, ClassifierTaxonomySHA256: taxonomySHA,
	}}
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMM, Unavailable: unavailable}, decider,
		policy.NewResolver(set("claude-haiku-4-5"), set(providers.ProviderAnthropic), catalogRosterID, policy.ManagedProviderPolicy()))
	adapter.WithClassifierReleaseIdentity(releaseID, selectionID, taxonomySHA, policy.ClassifierVariantPrimary, "hmm-v5", pinnedArtifactSHA)

	_, err := adapter.Route(context.Background(), router.Request{})
	require.Error(t, err)
	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable)
	assert.NotErrorIs(t, err, unavailable, "an artifact mismatch must not be classified as a degradable classifier outage")
	assert.Equal(t, pinnedArtifactSHA, decider.query.ClassifierArtifactSHA256)
}

func TestSidecarRouterV5PinnedPreviewTakesRosterFromGoSelector(t *testing.T) {
	releaseID, selectionID, taxonomySHA, artifactSHA := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	pinnedRoster := strings.Repeat("9", 64)
	decider := &recordingPolicy{preview: policy.PreviewResult{
		SchemaVersion: policy.SchemaVersionV5, RouteID: "",
		PolicyArtifactID: "hmm-v5", PolicyArtifactSHA256: artifactSHA,
		ClassifierReleaseID: releaseID, ClassifierSelectionID: selectionID,
		ClassifierVariantID: policy.ClassifierVariantPrimary, ClassifierTaxonomySHA256: taxonomySHA,
		PredictedLabel: "maximum", ClassOrder: []string{"maximum", "low"},
		ClassProbabilities: map[string]float64{"maximum": 0.8, "low": 0.2},
	}}
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMM, Unavailable: errors.New("unavailable")}, decider,
		policy.NewResolver(set("claude-opus-4-8", "claude-sonnet-5"), set(providers.ProviderAnthropic),
			func(model catalog.Model) string { return "anthropic/" + model.ID }, policy.ManagedProviderPolicy()))
	adapter.WithClassifierReleaseIdentity(releaseID, selectionID, taxonomySHA, policy.ClassifierVariantPrimary, "hmm-v5", artifactSHA)
	adapter.WithArmSelector(func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		assert.Equal(t, pinnedRoster, input.RosterSHA256)
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-sonnet-5", RosterSHA256: input.RosterSHA256, RankedFallback: classifierFallback("maximum")}, nil
	})
	ctx := router.WithPolicyPinRequest(context.Background(), router.PolicyPinRequest{
		Pin: router.PolicyPin{ArtifactSHA256: artifactSHA, RosterSHA256: pinnedRoster}, Authorized: true,
	})

	preview, err := adapter.PreviewRoute(ctx, router.Request{})
	require.NoError(t, err, "v5 returns no roster digest; the pinned roster is enforced on the Go selection")
	assert.Equal(t, "maximum", preview.SelectedGroup)
}
