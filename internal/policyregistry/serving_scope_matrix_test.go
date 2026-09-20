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
	binding := *store.objects[template.Binding].(*policyregistry.DeploymentBinding)
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
			base := *store.objects[initialSet.Default.Release].(*policyregistry.ServingRelease)
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
			initial, err := controller.Activate(context.Background(), store.publish(t, policyregistry.ServingProposals, initialProposal), "workflow", true)
			require.NoError(t, err)

			oldBinding := *store.objects[initialSet.Default.Binding].(*policyregistry.DeploymentBinding)
			oldBundle := *store.objects[base.Classifier].(*policyregistry.ClassifierBundle)
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
				profileRelease := *store.objects[initialSet.Profiles[profileKeyOne].Release].(*policyregistry.ServingRelease)
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
						profileRelease := *store.objects[previous.Release].(*policyregistry.ServingRelease)
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
			release := *store.objects[selection.Release].(*policyregistry.ServingRelease)
			release.Policy = nextPolicy
			profileRef := store.publish(t, policyregistry.ServingProfiles, policyregistry.RoutingProfile{
				SchemaVersion: policyregistry.ServingProfileV1,
				ProfileKey:    tamperedKey,
				Policy:        nextPolicy,
				Requirements:  release.Requirements,
			})
			binding := *store.objects[selection.Binding].(*policyregistry.DeploymentBinding)
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
			binding := *store.objects[set.Default.Binding].(*policyregistry.DeploymentBinding)
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
