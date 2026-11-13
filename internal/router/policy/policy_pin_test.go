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

var (
	pinnedArtifactSHA = strings.Repeat("a", 64)
	pinnedRosterSHA   = strings.Repeat("b", 64)
	otherRosterSHA    = strings.Repeat("c", 64)
)

func honouredPinContext() context.Context {
	return router.WithPolicyPinRequest(context.Background(), router.PolicyPinRequest{
		Pin:        router.PolicyPin{ArtifactSHA256: pinnedArtifactSHA, RosterSHA256: pinnedRosterSHA},
		Authorized: true,
	})
}

func newPinnedAdapter(t *testing.T, servedArtifactSHA string) (*policy.SidecarRouter, *recordingPolicy) {
	t.Helper()
	result := classifierOnlyResult()
	result.PolicyArtifactSHA256 = servedArtifactSHA
	result.RosterVersion = "sidecar-roster"
	decider := &recordingPolicy{result: result}
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "claude-sonnet-5"),
		set(providers.ProviderAnthropic),
		func(model catalog.Model) string { return "anthropic/" + model.ID },
		policy.ManagedProviderPolicy(),
	)
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy:    router.StrategyHMM,
		Unavailable: errors.New("selection unavailable"),
	}, decider, resolver)
	return adapter, decider
}

// rosterSelector fakes a boot-loaded roster set holding exactly the digests
// given; the first is the default served when no roster is pinned.
func rosterSelector(loaded ...string) policy.ArmSelector {
	return func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		for _, sha := range loaded {
			if sha == input.RosterSHA256 || input.RosterSHA256 == "" {
				return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-sonnet-5", RosterSHA256: sha}, nil
			}
		}
		return policy.SelectionPick{}, router.ErrPolicyPinUnavailable
	}
}

func TestPolicyPinHonouredServesExactlyThePinnedArtifactAndRoster(t *testing.T) {
	adapter, decider := newPinnedAdapter(t, pinnedArtifactSHA)
	adapter.WithArmSelector(rosterSelector(otherRosterSHA, pinnedRosterSHA))

	decision, err := adapter.Route(honouredPinContext(), router.Request{})

	require.NoError(t, err)
	assert.Equal(t, pinnedArtifactSHA, decider.query.ArtifactSHA256, "the sidecar must be asked for the pinned artifact")
	require.NotNil(t, decision.Metadata)
	assert.True(t, decision.Metadata.PolicyPinHonoured)
	assert.Equal(t, pinnedArtifactSHA, decision.Metadata.PolicyArtifactSHA256)
	assert.Equal(t, pinnedRosterSHA, decision.Metadata.RosterVersion, "the served roster identity must be the pinned digest")
}

func TestPolicyPinUnknownRosterFailsClosed(t *testing.T) {
	adapter, _ := newPinnedAdapter(t, pinnedArtifactSHA)
	adapter.WithArmSelector(rosterSelector(otherRosterSHA))

	_, err := adapter.Route(honouredPinContext(), router.Request{})

	require.ErrorIs(t, err, router.ErrPolicyPinUnavailable)
	assert.NotContains(t, err.Error(), "selection unavailable", "a pin failure is typed, not the generic strategy outage")
}

func TestPolicyPinArtifactMismatchFailsClosed(t *testing.T) {
	adapter, _ := newPinnedAdapter(t, strings.Repeat("d", 64))
	adapter.WithArmSelector(rosterSelector(pinnedRosterSHA))

	_, err := adapter.Route(honouredPinContext(), router.Request{})

	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable, "a sidecar serving a different artifact must never be passed off as the pin")
}

func TestPolicyPinRequiresRouterOwnedSelection(t *testing.T) {
	adapter, _ := newPinnedAdapter(t, pinnedArtifactSHA)

	_, err := adapter.Route(honouredPinContext(), router.Request{})

	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable)
}

func TestPolicyPinUnauthorizedRequestRoutesAsUnpinned(t *testing.T) {
	adapter, decider := newPinnedAdapter(t, strings.Repeat("d", 64))
	adapter.WithArmSelector(rosterSelector(otherRosterSHA))
	ctx := router.WithPolicyPinRequest(context.Background(), router.PolicyPinRequest{
		Pin: router.PolicyPin{ArtifactSHA256: pinnedArtifactSHA, RosterSHA256: pinnedRosterSHA},
	})

	decision, err := adapter.Route(ctx, router.Request{})

	require.NoError(t, err)
	assert.Empty(t, decider.query.ArtifactSHA256, "an unauthorized pin must not reach the sidecar")
	assert.False(t, decision.Metadata.PolicyPinHonoured)
	assert.Equal(t, "sidecar-roster", decision.Metadata.RosterVersion, "an unpinned turn keeps the sidecar's roster_version")
}

func TestUnpinnedReselectedTurnKeepsSidecarRosterVersion(t *testing.T) {
	adapter, _ := newPinnedAdapter(t, pinnedArtifactSHA)
	adapter.WithArmSelector(rosterSelector(otherRosterSHA))

	decision, err := adapter.Route(context.Background(), router.Request{})

	require.NoError(t, err)
	require.NotNil(t, decision.Metadata)
	assert.False(t, decision.Metadata.PolicyPinHonoured)
	assert.Equal(t, "sidecar-roster", decision.Metadata.RosterVersion, "Go reselection must not rewrite roster_version to the roster-file sha when no pin is honoured")
}

func TestPolicyPinPreviewCarriesArtifactAndRejectsMismatch(t *testing.T) {
	adapter, decider := newPinnedAdapter(t, pinnedArtifactSHA)
	decider.preview = policy.PreviewResult{SchemaVersion: policy.SchemaVersionV3, PolicyArtifactSHA256: strings.Repeat("d", 64)}

	_, err := adapter.PreviewRoute(honouredPinContext(), router.Request{})

	assert.Equal(t, pinnedArtifactSHA, decider.previewQuery.ArtifactSHA256)
	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable)
}
