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
