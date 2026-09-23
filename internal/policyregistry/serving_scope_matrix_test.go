package policyregistry_test

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
)

func publishChangedPolicy(t *testing.T, store *servingMemoryStore, base *rosterdata.Roster, bonus float64) policyregistry.PolicyObject {
	t.Helper()
	payload, err := rosterdata.CanonicalBytes(base)
	require.NoError(t, err)
	changed, err := rosterdata.ParseValidated(payload)
	require.NoError(t, err)
	changed.Preferences.PreferredModelBonus = bonus
	payload, err = rosterdata.CanonicalBytes(changed)
	require.NoError(t, err)
	digest := policyregistry.Digest(payload)
	ref := policyregistry.ObjectRef{
		URI:        testRegistryRoot + "/router_policy/v1/policies/sha256/" + digest + ".json",
		SHA256:     digest,
		Generation: 1,
	}
	store.policies[ref] = changed
	return policyregistry.PolicyObject{URI: ref.URI, SHA256: ref.SHA256, Generation: ref.Generation, SchemaVersion: changed.SchemaVersion}
}

func publishSelection(
	t *testing.T,
	store *servingMemoryStore,
	template policyregistry.ServingSelection,
	release policyregistry.ServingRelease,
	router policyregistry.RevisionBinding,
	classifier policyregistry.RevisionBinding,
	profile *policyregistry.ObjectRef,
) policyregistry.ServingSelection {
	t.Helper()
	releaseRef := store.publish(t, policyregistry.ServingReleases, release)
	binding := *store.object(t, policyregistry.ServingBindings, template.Binding).(*policyregistry.DeploymentBinding)
	binding.Release = releaseRef
	binding.Router = router
	binding.Classifier = classifier
	binding.ClassifierBundleSHA256 = release.Classifier.SHA256
	bindingRef := store.publish(t, policyregistry.ServingBindings, binding)
	return policyregistry.ServingSelection{Release: releaseRef, Binding: bindingRef, Profile: profile}
}

func TestServingProposalScopeMatrixPreservesCompleteProfileInventory(t *testing.T) {
	scopes := []policyregistry.ChangeScope{
		policyregistry.ChangeFull,
		policyregistry.ChangeRouter,
		policyregistry.ChangeRoster,
		policyregistry.ChangeClassifier,
		policyregistry.ChangeProfile,
		policyregistry.ChangeCustom,
	}
	for _, scope := range scopes {
		t.Run(string(scope), func(t *testing.T) {
			store, _, initialSet := controllerFixture(t)
			base := *store.object(t, policyregistry.ServingReleases, initialSet.Default.Release).(*policyregistry.ServingRelease)
			initialSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, initialSet.Default, profileKeyOne, base.Policy)
			secondPolicy := publishChangedPolicy(t, store, store.policies[policyregistry.ObjectRef{
				URI: base.Policy.URI, SHA256: base.Policy.SHA256, Generation: base.Policy.Generation,
			}], 0.55)
			initialSet.Profiles[profileKeyTwo] = registerProfileFixture(t, store, initialSet.Default, profileKeyTwo, secondPolicy)
			store.publish(t, policyregistry.ServingSelectionSets, initialSet)
			controller, err := policyregistry.NewServingController(
				store,
				preparedValidator(func(context.Context, policyregistry.PreparedSelection) error { return nil }),
				func() time.Time { return servingEpoch },
				slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			require.NoError(t, err)
			initialProposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, initialSet, servingEpoch)
			initial, err := controller.Activate(context.Background(), store.publish(t, policyregistry.ServingProposals, initialProposal), "workflow")
			require.NoError(t, err)

			oldBinding := *store.object(t, policyregistry.ServingBindings, initialSet.Default.Binding).(*policyregistry.DeploymentBinding)
			oldBundle := *store.object(t, policyregistry.ServingClassifiers, base.Classifier).(*policyregistry.ClassifierBundle)
			nextBundle := oldBundle
			nextBundle.Identity.ArtifactID = "fixture-next"
			nextBundle.Package = artifactRef("classifier-package-next")
			nextBundle.Identity.PackageSHA256 = nextBundle.Package.SHA256
			nextBundle.Configuration = artifactRef("classifier-config-next")
			nextBundle.AuxiliaryModels = maps.Clone(oldBundle.AuxiliaryModels)
			nextClassifier := store.publish(t, policyregistry.ServingClassifiers, nextBundle)
			nextPolicy := publishChangedPolicy(t, store, store.policies[policyregistry.ObjectRef{
				URI: base.Policy.URI, SHA256: base.Policy.SHA256, Generation: base.Policy.Generation,
			}], 0.65)
			source := base
			source.RouterImageDigest = "sha256:" + strings.Repeat("4", 64)
			source.Policy = nextPolicy
			source.Classifier = nextClassifier
			sourceRef := store.publish(t, policyregistry.ServingReleases, source)

			nextSet := initialSet
			nextSet.Profiles = maps.Clone(initialSet.Profiles)
			nextRouter := oldBinding.Router
			nextClassifierBinding := oldBinding.Classifier
			defaultRelease := base
			switch scope {
			case policyregistry.ChangeFull, policyregistry.ChangeCustom:
				defaultRelease = source
				nextRouter.Name = "worker-0002"
				nextRouter.ImageDigest = source.RouterImageDigest
				nextClassifierBinding.Name = "classifier-0002"
				nextClassifierBinding.ImageDigest = nextBundle.Identity.ImageDigest
				nextClassifierBinding.Configuration = nextBundle.Configuration
			case policyregistry.ChangeRouter:
				defaultRelease.RouterImageDigest = source.RouterImageDigest
				nextRouter.Name = "worker-0002"
				nextRouter.ImageDigest = source.RouterImageDigest
			case policyregistry.ChangeRoster:
				defaultRelease.Policy = source.Policy
			case policyregistry.ChangeClassifier:
				defaultRelease.Classifier = source.Classifier
				nextClassifierBinding.Name = "classifier-0002"
				nextClassifierBinding.ImageDigest = nextBundle.Identity.ImageDigest
				nextClassifierBinding.Configuration = nextBundle.Configuration
			case policyregistry.ChangeProfile:
				profileRelease := *store.object(t, policyregistry.ServingReleases, initialSet.Profiles[profileKeyOne].Release).(*policyregistry.ServingRelease)
				profileRelease.Policy = source.Policy
				profileRef := store.publish(t, policyregistry.ServingProfiles, policyregistry.RoutingProfile{
					SchemaVersion: policyregistry.ServingProfileV1,
					ProfileKey:    profileKeyOne,
					Policy:        source.Policy,
					Requirements:  profileRelease.Requirements,
				})
				nextSet.Profiles[profileKeyOne] = publishSelection(t, store, initialSet.Profiles[profileKeyOne], profileRelease, oldBinding.Router, oldBinding.Classifier, &profileRef)
			}
			if scope != policyregistry.ChangeProfile {
				if scope == policyregistry.ChangeFull {
					nextSet.Default = publishSelection(t, store, initialSet.Default, source, nextRouter, nextClassifierBinding, nil)
					nextSet.Default.Release = sourceRef
				} else {
					nextSet.Default = publishSelection(t, store, initialSet.Default, defaultRelease, nextRouter, nextClassifierBinding, nil)
				}
				if scope != policyregistry.ChangeRoster {
					for key, previous := range initialSet.Profiles {
						profileRelease := *store.object(t, policyregistry.ServingReleases, previous.Release).(*policyregistry.ServingRelease)
						profileRelease.RouterImageDigest = defaultRelease.RouterImageDigest
						profileRelease.Classifier = defaultRelease.Classifier
						nextSet.Profiles[key] = publishSelection(t, store, previous, profileRelease, nextRouter, nextClassifierBinding, previous.Profile)
					}
				}
			}
			store.publish(t, policyregistry.ServingSelectionSets, nextSet)
			proposal := fixtureProposal(t, initial.Snapshot, nextSet, servingEpoch)
			proposal.Scope = scope
			proposal.SourceRelease = sourceRef
			if scope == policyregistry.ChangeProfile {
				proposal.ProfileKey = profileKeyOne
				proposal.SourceRelease = nextSet.Profiles[profileKeyOne].Release
			}
			require.NoError(t, controller.ValidateProposal(context.Background(), proposal))
			if scope == policyregistry.ChangeProfile {
				require.NotEqual(t, initialSet.Profiles[profileKeyOne].Profile, nextSet.Profiles[profileKeyOne].Profile)
			} else {
				require.Equal(t, initialSet.Profiles[profileKeyOne].Profile, nextSet.Profiles[profileKeyOne].Profile)
			}
			require.Equal(t, initialSet.Profiles[profileKeyTwo].Profile, nextSet.Profiles[profileKeyTwo].Profile)

			tampered := nextSet
			tampered.Profiles = maps.Clone(nextSet.Profiles)
			tamperedKey := profileKeyOne
			expectedError := "retain destination profile revisions"
			if scope == policyregistry.ChangeProfile {
				tamperedKey = profileKeyTwo
				expectedError = "out-of-scope customer tuple"
			}
			selection := tampered.Profiles[tamperedKey]
			release := *store.object(t, policyregistry.ServingReleases, selection.Release).(*policyregistry.ServingRelease)
			release.Policy = nextPolicy
			profileRef := store.publish(t, policyregistry.ServingProfiles, policyregistry.RoutingProfile{
				SchemaVersion: policyregistry.ServingProfileV1,
				ProfileKey:    tamperedKey,
				Policy:        nextPolicy,
				Requirements:  release.Requirements,
			})
			binding := *store.object(t, policyregistry.ServingBindings, selection.Binding).(*policyregistry.DeploymentBinding)
			tampered.Profiles[tamperedKey] = publishSelection(t, store, selection, release, binding.Router, binding.Classifier, &profileRef)
			proposal.SelectionSet = store.publish(t, policyregistry.ServingSelectionSets, tampered)
			require.ErrorContains(t, controller.ValidateProposal(context.Background(), proposal), expectedError)
		})
	}
}

func TestServingProposalTargetMatrixRejectsCrossTargetBindings(t *testing.T) {
	for _, target := range []policyregistry.ServingTarget{
		policyregistry.TargetStaging,
		policyregistry.TargetStable,
		policyregistry.TargetInternal,
	} {
		t.Run(string(target), func(t *testing.T) {
			store, _, set := controllerFixture(t)
			set.Target = target
			binding := *store.object(t, policyregistry.ServingBindings, set.Default.Binding).(*policyregistry.DeploymentBinding)
			binding.Target = target
			set.Default.Binding = store.publish(t, policyregistry.ServingBindings, binding)
			store.publish(t, policyregistry.ServingSelectionSets, set)
			controller, err := policyregistry.NewServingController(
				store,
				preparedValidator(func(context.Context, policyregistry.PreparedSelection) error { return nil }),
				func() time.Time { return servingEpoch },
				slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			require.NoError(t, err)
			proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
			require.NoError(t, controller.ValidateProposal(context.Background(), proposal))

			wrongTarget := policyregistry.TargetStable
			if target == wrongTarget {
				wrongTarget = policyregistry.TargetStaging
			}
			proposal.Target = wrongTarget
			require.ErrorContains(t, controller.ValidateProposal(context.Background(), proposal), "targets differ")

			proposal.Target = target
			wrongBinding := binding
			wrongBinding.Target = wrongTarget
			set.Default.Binding = store.publish(t, policyregistry.ServingBindings, wrongBinding)
			proposal.SelectionSet = store.publish(t, policyregistry.ServingSelectionSets, set)
			require.ErrorContains(t, controller.ValidateProposal(context.Background(), proposal), "deployment binding differs")
		})
	}
}

func permissiveController(t *testing.T, store *servingMemoryStore) *policyregistry.ServingController {
	t.Helper()
	controller, err := policyregistry.NewServingController(
		store,
		preparedValidator(func(context.Context, policyregistry.PreparedSelection) error { return nil }),
		func() time.Time { return servingEpoch },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	require.NoError(t, err)
	return controller
}

// heterogeneousLaneFixture activates a Default lane plus two profiles whose policies differ from
// each other, mirroring a target where customers run lane-local rosters.
func heterogeneousLaneFixture(t *testing.T) (*servingMemoryStore, *policyregistry.ServingController, policyregistry.SelectionSet, policyregistry.ActivationResult) {
	t.Helper()
	store, _, initialSet := controllerFixture(t)
	base := *store.object(t, policyregistry.ServingReleases, initialSet.Default.Release).(*policyregistry.ServingRelease)
	basePolicy := store.policies[policyregistry.ObjectRef{URI: base.Policy.URI, SHA256: base.Policy.SHA256, Generation: base.Policy.Generation}]
	initialSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, initialSet.Default, profileKeyOne, base.Policy)
	initialSet.Profiles[profileKeyTwo] = registerProfileFixture(t, store, initialSet.Default, profileKeyTwo, publishChangedPolicy(t, store, basePolicy, 0.55))
	store.publish(t, policyregistry.ServingSelectionSets, initialSet)
	controller := permissiveController(t, store)
	initialProposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, initialSet, servingEpoch)
	initial, err := controller.Activate(context.Background(), store.publish(t, policyregistry.ServingProposals, initialProposal), "workflow")
	require.NoError(t, err)
	return store, controller, initialSet, initial
}

// routerOnlySource publishes a source release that carries a new Router image and provenance
// alongside a foreign policy and classifier, which a Router-only proposal must ignore.
func routerOnlySource(t *testing.T, store *servingMemoryStore, initialSet policyregistry.SelectionSet, digit string) (policyregistry.ServingRelease, policyregistry.ObjectRef) {
	t.Helper()
	base := *store.object(t, policyregistry.ServingReleases, initialSet.Default.Release).(*policyregistry.ServingRelease)
	basePolicy := store.policies[policyregistry.ObjectRef{URI: base.Policy.URI, SHA256: base.Policy.SHA256, Generation: base.Policy.Generation}]
	source := base
	source.RouterImageDigest = "sha256:" + strings.Repeat(digit, 64)
	source.Policy = publishChangedPolicy(t, store, basePolicy, 0.65)
	source.Provenance.RouterRevision = strings.Repeat(digit, 40)
	source.Provenance.WeaveRevision = strings.Repeat("a", 40)
	attestation := "build-attestation-" + digit
	source.Provenance.BuildAttestation = artifactRef(attestation)
	store.artifacts[source.Provenance.BuildAttestation] = []byte(attestation)
	return source, store.publish(t, policyregistry.ServingReleases, source)
}

func routerOnlyRelease(t *testing.T, store *servingMemoryStore, previous policyregistry.ServingSelection, source policyregistry.ServingRelease) policyregistry.ServingRelease {
	release := *store.object(t, policyregistry.ServingReleases, previous.Release).(*policyregistry.ServingRelease)
	release.RouterImageDigest = source.RouterImageDigest
	release.Provenance = source.Provenance
	return release
}

func sharedRouterRevision(t *testing.T, store *servingMemoryStore, initialSet policyregistry.SelectionSet, source policyregistry.ServingRelease, name string) policyregistry.RevisionBinding {
	router := store.object(t, policyregistry.ServingBindings, initialSet.Default.Binding).(*policyregistry.DeploymentBinding).Router
	router.Name = name
	router.URL = "https://" + name + ".example"
	router.ImageDigest = source.RouterImageDigest
	return router
}

// routerOnlyLane publishes the canonical successor lane: same policy, classifier, requirements,
// project, region, and classifier revision; new release, binding, and shared Router revision.
func routerOnlyLane(t *testing.T, store *servingMemoryStore, previous policyregistry.ServingSelection, source policyregistry.ServingRelease, router policyregistry.RevisionBinding) policyregistry.ServingSelection {
	t.Helper()
	classifier := store.object(t, policyregistry.ServingBindings, previous.Binding).(*policyregistry.DeploymentBinding).Classifier
	return publishSelection(t, store, previous, routerOnlyRelease(t, store, previous, source), router, classifier, previous.Profile)
}

func routerOnlySet(t *testing.T, store *servingMemoryStore, initialSet policyregistry.SelectionSet, source policyregistry.ServingRelease, router policyregistry.RevisionBinding) policyregistry.SelectionSet {
	t.Helper()
	nextSet := initialSet
	nextSet.Profiles = make(map[string]policyregistry.ServingSelection, len(initialSet.Profiles))
	nextSet.Default = routerOnlyLane(t, store, initialSet.Default, source, router)
	for key, previous := range initialSet.Profiles {
		nextSet.Profiles[key] = routerOnlyLane(t, store, previous, source, router)
	}
	return nextSet
}

func routerOnlyProposal(t *testing.T, store *servingMemoryStore, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, sourceRef policyregistry.ObjectRef) policyregistry.DeploymentProposal {
	t.Helper()
	store.publish(t, policyregistry.ServingSelectionSets, set)
	proposal := fixtureProposal(t, snapshot, set, servingEpoch)
	proposal.Scope = policyregistry.ChangeRouter
	proposal.SourceRelease = sourceRef
	return proposal
}

func TestServingProposalScopeMatrixRouterOnlyUpdatesEveryLaneAtomically(t *testing.T) {
	ctx := context.Background()
	store, controller, initialSet, initial := heterogeneousLaneFixture(t)
	source, sourceRef := routerOnlySource(t, store, initialSet, "4")
	nextRouter := sharedRouterRevision(t, store, initialSet, source, "worker-0002")
	nextSet := routerOnlySet(t, store, initialSet, source, nextRouter)
	proposal := routerOnlyProposal(t, store, initial.Snapshot, nextSet, sourceRef)
	require.NoError(t, controller.ValidateProposal(ctx, proposal))
	forward, err := controller.Activate(ctx, store.publish(t, policyregistry.ServingProposals, proposal), "workflow")
	require.NoError(t, err)
	require.Equal(t, proposal.SelectionSet, forward.Activation.SelectionSet)

	lanes := map[string][2]policyregistry.ServingSelection{"default": {initialSet.Default, nextSet.Default}}
	for key, previous := range initialSet.Profiles {
		lanes[key] = [2]policyregistry.ServingSelection{previous, nextSet.Profiles[key]}
	}
	require.Len(t, lanes, 3)
	for lane, pair := range lanes {
		previous, next := pair[0], pair[1]
		require.NotEqual(t, previous.Release, next.Release, lane)
		require.NotEqual(t, previous.Binding, next.Binding, lane)
		require.Equal(t, previous.Profile, next.Profile, lane)
		previousRelease := *store.object(t, policyregistry.ServingReleases, previous.Release).(*policyregistry.ServingRelease)
		nextRelease := *store.object(t, policyregistry.ServingReleases, next.Release).(*policyregistry.ServingRelease)
		require.Equal(t, source.RouterImageDigest, nextRelease.RouterImageDigest, lane)
		require.Equal(t, source.Provenance, nextRelease.Provenance, lane)
		require.Equal(t, previousRelease.SchemaVersion, nextRelease.SchemaVersion, lane)
		require.Equal(t, previousRelease.Policy, nextRelease.Policy, lane)
		require.Equal(t, previousRelease.Classifier, nextRelease.Classifier, lane)
		require.Equal(t, previousRelease.Requirements, nextRelease.Requirements, lane)
		previousBinding := *store.object(t, policyregistry.ServingBindings, previous.Binding).(*policyregistry.DeploymentBinding)
		nextBinding := *store.object(t, policyregistry.ServingBindings, next.Binding).(*policyregistry.DeploymentBinding)
		require.Equal(t, nextRouter, nextBinding.Router, lane)
		require.NotEqual(t, previousBinding.Router, nextBinding.Router, lane)
		require.Equal(t, previousBinding.Target, nextBinding.Target, lane)
		require.Equal(t, previousBinding.Project, nextBinding.Project, lane)
		require.Equal(t, previousBinding.Region, nextBinding.Region, lane)
		require.Equal(t, previousBinding.Classifier, nextBinding.Classifier, lane)
		require.Equal(t, previousBinding.ClassifierBundleSHA256, nextBinding.ClassifierBundleSHA256, lane)
	}
	defaultPolicy := store.object(t, policyregistry.ServingReleases, nextSet.Default.Release).(*policyregistry.ServingRelease).Policy
	require.NotEqual(t, defaultPolicy, store.object(t, policyregistry.ServingReleases, nextSet.Profiles[profileKeyTwo].Release).(*policyregistry.ServingRelease).Policy, "heterogeneous lane policies survive")
	require.NotEqual(t, source.Policy, defaultPolicy, "source policy must not leak into the default lane")

	t.Run("exact same-target rollback restores heterogeneous lanes", func(t *testing.T) {
		rollbackProposal := fixtureProposal(t, forward.Snapshot, initialSet, servingEpoch)
		rollbackProposal.Scope = policyregistry.ChangeRollback
		rollbackRef := store.publish(t, policyregistry.ServingProposals, rollbackProposal)
		prepared, err := controller.Prepare(ctx, rollbackRef)
		require.NoError(t, err)
		require.True(t, prepared.Prepared)
		rollback, err := controller.Activate(ctx, rollbackRef, "workflow")
		require.NoError(t, err)
		require.Equal(t, initial.Activation.SelectionSet, rollback.Activation.SelectionSet)
		require.Greater(t, rollback.Snapshot.Generation, forward.Snapshot.Generation)

		crossTarget := fixtureProposal(t, rollback.Snapshot, initialSet, servingEpoch)
		crossTarget.Scope = policyregistry.ChangeRollback
		crossTarget.Target = policyregistry.TargetStaging
		require.ErrorContains(t, controller.ValidateProposal(ctx, crossTarget), "targets differ")

		neverActivated := routerOnlySet(t, store, initialSet, source, sharedRouterRevision(t, store, initialSet, source, "worker-0009"))
		unserved := routerOnlyProposal(t, store, rollback.Snapshot, neverActivated, neverActivated.Default.Release)
		unserved.Scope = policyregistry.ChangeRollback
		require.ErrorContains(t, controller.ValidateProposal(ctx, unserved), "previously activated on the same target")
	})
}

func TestServingProposalScopeMatrixRouterOnlyRejectsPartialOrDriftingLanes(t *testing.T) {
	ctx := context.Background()
	store, controller, initialSet, initial := heterogeneousLaneFixture(t)
	source, sourceRef := routerOnlySource(t, store, initialSet, "4")
	nextRouter := sharedRouterRevision(t, store, initialSet, source, "worker-0002")
	canonical := routerOnlySet(t, store, initialSet, source, nextRouter)
	nextClassifier := store.object(t, policyregistry.ServingBindings, initialSet.Default.Binding).(*policyregistry.DeploymentBinding).Classifier
	basePolicy := store.policies[policyregistry.ObjectRef{
		URI: source.Policy.URI, SHA256: source.Policy.SHA256, Generation: source.Policy.Generation,
	}]

	cases := []struct {
		name     string
		mutate   func(set *policyregistry.SelectionSet)
		expected string
	}{
		{
			name: "subset update leaves a profile on the predecessor router",
			mutate: func(set *policyregistry.SelectionSet) {
				set.Profiles[profileKeyTwo] = initialSet.Profiles[profileKeyTwo]
			},
			expected: "profile must share its lane's worker image",
		},
		{
			name: "default lane retains predecessor release and binding",
			mutate: func(set *policyregistry.SelectionSet) {
				set.Default = initialSet.Default
			},
			expected: "profile must share its lane's worker image",
		},
		{
			name: "lane addition",
			mutate: func(set *policyregistry.SelectionSet) {
				added := routerOnlyLane(t, store, registerProfileFixture(t, store, initialSet.Default, "10000000-0000-4000-8000-000000000003", source.Policy), source, nextRouter)
				set.Profiles["10000000-0000-4000-8000-000000000003"] = added
			},
			expected: "explicit named-profile proposal",
		},
		{
			name: "lane removal",
			mutate: func(set *policyregistry.SelectionSet) {
				delete(set.Profiles, profileKeyOne)
			},
			expected: "registered profile keys cannot be removed",
		},
		{
			name: "profile lane on a different router revision",
			mutate: func(set *policyregistry.SelectionSet) {
				other := nextRouter
				other.Name = "worker-0003"
				set.Profiles[profileKeyOne] = routerOnlyLane(t, store, initialSet.Profiles[profileKeyOne], source, other)
			},
			expected: "reuse its lane's prepared worker and classifier revisions",
		},
		{
			name: "profile lane on an incompatible router configuration",
			mutate: func(set *policyregistry.SelectionSet) {
				other := nextRouter
				other.Configuration = artifactRef("worker-config-other")
				set.Profiles[profileKeyOne] = routerOnlyLane(t, store, initialSet.Profiles[profileKeyOne], source, other)
			},
			expected: "reuse its lane's prepared worker and classifier revisions",
		},
		{
			name: "default lane policy drift",
			mutate: func(set *policyregistry.SelectionSet) {
				release := routerOnlyRelease(t, store, initialSet.Default, source)
				release.Policy = publishChangedPolicy(t, store, basePolicy, 0.75)
				set.Default = publishSelection(t, store, initialSet.Default, release, nextRouter, nextClassifier, nil)
			},
			expected: "retain destination policy, classifier, and requirements",
		},
		{
			name: "profile lane policy drift",
			mutate: func(set *policyregistry.SelectionSet) {
				previous := initialSet.Profiles[profileKeyOne]
				release := routerOnlyRelease(t, store, previous, source)
				release.Policy = publishChangedPolicy(t, store, basePolicy, 0.75)
				profileRef := store.publish(t, policyregistry.ServingProfiles, policyregistry.RoutingProfile{
					SchemaVersion: policyregistry.ServingProfileV1,
					ProfileKey:    profileKeyOne,
					Policy:        release.Policy,
					Requirements:  release.Requirements,
				})
				set.Profiles[profileKeyOne] = publishSelection(t, store, previous, release, nextRouter, nextClassifier, &profileRef)
			},
			expected: "retain destination profile revisions",
		},
		{
			name: "profile lane provenance drift",
			mutate: func(set *policyregistry.SelectionSet) {
				previous := initialSet.Profiles[profileKeyOne]
				release := routerOnlyRelease(t, store, previous, source)
				release.Provenance.WeaveRevision = strings.Repeat("b", 40)
				set.Profiles[profileKeyOne] = publishSelection(t, store, previous, release, nextRouter, nextClassifier, previous.Profile)
			},
			expected: "carry the source Router image and provenance",
		},
		{
			name: "profile lane relocates region",
			mutate: func(set *policyregistry.SelectionSet) {
				previous := initialSet.Profiles[profileKeyOne]
				lane := routerOnlyLane(t, store, previous, source, nextRouter)
				binding := *store.object(t, policyregistry.ServingBindings, lane.Binding).(*policyregistry.DeploymentBinding)
				binding.Region = "other-region"
				lane.Binding = store.publish(t, policyregistry.ServingBindings, binding)
				set.Profiles[profileKeyOne] = lane
			},
			expected: "retain destination target, project, region, and classifier revision",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			set := canonical
			set.Profiles = maps.Clone(canonical.Profiles)
			test.mutate(&set)
			proposal := routerOnlyProposal(t, store, initial.Snapshot, set, sourceRef)
			require.ErrorContains(t, controller.ValidateProposal(ctx, proposal), test.expected)
		})
	}

	t.Run("canonical set still admitted after rejected variants", func(t *testing.T) {
		require.NoError(t, controller.ValidateProposal(ctx, routerOnlyProposal(t, store, initial.Snapshot, canonical, sourceRef)))
	})
}

func TestServingProposalScopeMatrixRouterOnlyRejectsHeterogeneousPredecessorConfiguration(t *testing.T) {
	ctx := context.Background()
	store, _, initialSet := controllerFixture(t)
	base := *store.object(t, policyregistry.ServingReleases, initialSet.Default.Release).(*policyregistry.ServingRelease)
	initialSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, initialSet.Default, profileKeyOne, base.Policy)
	drifted := initialSet.Profiles[profileKeyOne]
	driftedBinding := *store.object(t, policyregistry.ServingBindings, drifted.Binding).(*policyregistry.DeploymentBinding)
	driftedBinding.Router.Configuration = artifactRef("worker-config-legacy")
	drifted.Binding = store.publish(t, policyregistry.ServingBindings, driftedBinding)
	initialSet.Profiles[profileKeyOne] = drifted
	store.publish(t, policyregistry.ServingSelectionSets, initialSet)
	snapshot, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, initialSet, servingEpoch)
	store.states[initialSet.Target] = snapshot
	controller := permissiveController(t, store)

	source, sourceRef := routerOnlySource(t, store, initialSet, "5")
	nextSet := routerOnlySet(t, store, initialSet, source, sharedRouterRevision(t, store, initialSet, source, "worker-0002"))
	proposal := routerOnlyProposal(t, store, snapshot, nextSet, sourceRef)
	require.ErrorContains(t, controller.ValidateProposal(ctx, proposal), "identical predecessor Router configuration across lanes")
}
