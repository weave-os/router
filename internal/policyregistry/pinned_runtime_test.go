package policyregistry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
)

type pinnedDecisionRouter struct {
	model string
}

func (r pinnedDecisionRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{Model: r.model}, nil
}

func pinnedFixture(t *testing.T, profile string) (*servingMemoryStore, policyregistry.PinnedRuntimeRequest) {
	t.Helper()
	store, set := cohortFixture(t)
	ref := store.publishArtifact(t, policyregistry.ServingSelectionSet, set)
	selection := set.View(ref).Default
	if profile != "" {
		selection = set.View(ref).Profiles[profile]
	}
	prepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, profile, selection)
	require.NoError(t, err)
	identity := prepared.Candidate.Classifier.Identity
	return store, policyregistry.PinnedRuntimeRequest{Target: set.Target, ProfileKey: profile, SelectionSet: ref,
		RouterRevision: prepared.Candidate.Provenance.RouterRevision, PolicySHA256: prepared.PolicyReference.SHA256,
		ClassifierArtifactID: identity.ArtifactID, ClassifierPackageSHA256: identity.PackageSHA256,
		ClassifierImageDigest: identity.ImageDigest, ClassifierURL: "http://127.0.0.1:8093"}
}

func TestPinnedV3RuntimePreservesProfileAndNeverConsultsMutableHead(t *testing.T) {
	store, pin := pinnedFixture(t, profileKeyTwo)
	store.readErr = errors.New("mutable heads must never be read")
	build := func(_ context.Context, candidate policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		require.Equal(t, pin.PolicySHA256, candidate.Release.Policy.SHA256)
		require.Equal(t, pin.ClassifierURL, candidate.HeadSnapshot.Head.ClassifierRevisionURL)
		require.Empty(t, candidate.ClassifierAudience)
		return map[router.Strategy]router.Router{router.PrimaryRoutingStrategy: pinnedDecisionRouter{model: candidate.Policy.AllArms()[0]}}, nil
	}
	loaded, err := policyregistry.LoadPinnedRuntime(context.Background(), store, pin, build)
	require.NoError(t, err)
	admitted := policyregistry.NewAdmittedRouter(router.PrimaryRoutingStrategy, loaded.Snapshot)
	_, err = admitted.Route(context.Background(), router.Request{})
	require.Error(t, err, "bootstrap is not a request fallback")
	decision, err := admitted.Route(loaded.Bind(context.Background()), router.Request{})
	require.NoError(t, err)
	require.Equal(t, alternateRosterArm, decision.Model, "profile must not become candidate/default roster")
	require.Equal(t, pin.SelectionSet, loaded.Selection.Binding)
	require.Zero(t, store.casCalls)
}

func TestPinnedRuntimeRejectsIdentityDriftBeforeBuilding(t *testing.T) {
	for name, mutate := range map[string]func(*policyregistry.PinnedRuntimeRequest){
		"source":     func(p *policyregistry.PinnedRuntimeRequest) { p.RouterRevision = strings.Repeat("f", 40) },
		"policy":     func(p *policyregistry.PinnedRuntimeRequest) { p.PolicySHA256 = strings.Repeat("f", 64) },
		"classifier": func(p *policyregistry.PinnedRuntimeRequest) { p.ClassifierPackageSHA256 = strings.Repeat("f", 64) },
		"image": func(p *policyregistry.PinnedRuntimeRequest) {
			p.ClassifierImageDigest = "sha256:" + strings.Repeat("f", 64)
		},
		"artifact":          func(p *policyregistry.PinnedRuntimeRequest) { p.ClassifierArtifactID = "other_classifier" },
		"target":            func(p *policyregistry.PinnedRuntimeRequest) { p.Target = policyregistry.TargetInternal },
		"profile":           func(p *policyregistry.PinnedRuntimeRequest) { p.ProfileKey = "missing" },
		"remote classifier": func(p *policyregistry.PinnedRuntimeRequest) { p.ClassifierURL = "https://example.com" },
		"credentials":       func(p *policyregistry.PinnedRuntimeRequest) { p.ClassifierURL = "http://secret@127.0.0.1:8093" },
	} {
		t.Run(name, func(t *testing.T) {
			store, pin := pinnedFixture(t, "")
			mutate(&pin)
			_, err := policyregistry.LoadPinnedRuntime(context.Background(), store, pin, func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
				t.Fatal("invalid identities reached classifier/runtime construction")
				return nil, nil
			})
			require.Error(t, err)
			require.Zero(t, store.casCalls)
		})
	}
}
