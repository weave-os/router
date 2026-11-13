package policyregistry_test

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

func cohortCache(t *testing.T, store *servingMemoryStore) *policyregistry.ServingRuntimeCache {
	t.Helper()
	cache, err := policyregistry.NewServingRuntimeCache(store, func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		return map[router.Strategy]router.Router{router.StrategyHMM: stubRouter{}}, nil
	})
	require.NoError(t, err)
	return cache
}

func TestCohortCustomersShareCodeWithIsolatedConcurrentRosters(t *testing.T) {
	store, set := cohortFixture(t)
	ref := store.publishArtifact(t, policyregistry.ServingSelectionSet, set)
	view := set.View(ref)
	var builds atomic.Int64
	cache, err := policyregistry.NewServingRuntimeCache(store, func(_ context.Context, candidate policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		builds.Add(1)
		if candidate.Release.Policy != set.Profiles[profileKeyOne] && candidate.Release.Policy != set.Profiles[profileKeyTwo] {
			return nil, fmt.Errorf("builder received unassigned policy %s", candidate.Release.Policy.SHA256)
		}
		return map[router.Strategy]router.Router{router.StrategyHMM: stubRouter{}}, nil
	})
	require.NoError(t, err)
	admissions := []policyregistry.SessionReleaseBinding{
		{Target: set.Target, ProfileKey: profileKeyOne, Selection: view.Profiles[profileKeyOne]},
		{Target: set.Target, ProfileKey: profileKeyTwo, Selection: view.Profiles[profileKeyTwo]},
	}
	expectedArms := []string{"openai/gpt-5.6-sol", alternateRosterArm}
	// First wave is cold; second is warm. Both customers use the same candidate and revision.
	for range 2 {
		var requests errgroup.Group
		for index := range 64 {
			index := index % len(admissions)
			requests.Go(func() error {
				snapshot, err := cache.Snapshot(context.Background(), admissions[index])
				if err != nil {
					return err
				}
				ctx := policyregistry.WithServingSnapshot(context.Background(), snapshot)
				arms, err := (policyregistry.AdmittedRosterSource{}).Roster(ctx)
				if err != nil {
					return err
				}
				if len(arms) != 1 || arms[0] != expectedArms[index] {
					return fmt.Errorf("customer %d received %v", index, arms)
				}
				if snapshot.Release.Policy != set.Profiles[admissions[index].ProfileKey] {
					return fmt.Errorf("customer %d policy attribution differs", index)
				}
				if snapshot.HeadSnapshot.Head.ReleaseSHA256 != set.Code.Candidate.SHA256 {
					return fmt.Errorf("customer %d code attribution differs", index)
				}
				return nil
			})
		}
		require.NoError(t, requests.Wait())
	}
	first, err := cache.Snapshot(context.Background(), admissions[0])
	require.NoError(t, err)
	second, err := cache.Snapshot(context.Background(), admissions[1])
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.NotEqual(t, first.Release.Policy, second.Release.Policy)

	// LRU refresh reloads immutable historical refs, never a newer assignment or another profile.
	populated := set
	populated.Profiles = maps.Clone(set.Profiles)
	for range 140 {
		populated.Profiles[uuid.NewString()] = set.DefaultPolicy
	}
	populatedRef := store.publishArtifact(t, policyregistry.ServingSelectionSet, populated)
	for key, selection := range populated.View(populatedRef).Profiles {
		_, err := cache.Snapshot(context.Background(), policyregistry.SessionReleaseBinding{Target: set.Target, ProfileKey: key, Selection: selection})
		require.NoError(t, err)
	}
	beforeReload := builds.Load()
	refreshed, err := cache.Snapshot(context.Background(), admissions[1])
	require.NoError(t, err)
	require.Greater(t, builds.Load(), beforeReload)
	require.NotSame(t, second, refreshed)
	require.Equal(t, second.Candidate, refreshed.Candidate)
	require.Equal(t, []string{alternateRosterArm}, refreshed.Policy.AllArms())
}

func TestCohortPromotionAndRosterRollbackRetainConversationAssignments(t *testing.T) {
	store, set := cohortFixture(t)
	controller := permissiveController(t, store)
	initialProposal := cohortProposal(t, store, set, nil, policyregistry.ChangeFull, "")
	initial, err := controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, initialProposal), "workflow")
	require.NoError(t, err)
	admission := policyregistry.ServingAdmission{Store: store}
	projection := policyregistry.AdmissionProjection{Target: set.Target, ProfileKey: profileKeyOne, AssignmentGeneration: 1}
	decide := func(previous *policyregistry.SessionReleaseBinding) policyregistry.SessionReleaseBinding {
		t.Helper()
		binding, err := admission.Decide(context.Background(), policyregistry.SerializedAdmission{Projection: projection, Previous: previous, Clock: func(context.Context) (time.Time, error) { return servingEpoch.Add(time.Hour), nil }})
		require.NoError(t, err)
		return binding
	}
	oldConversation := decide(nil)
	updated := set
	updated.Profiles = maps.Clone(set.Profiles)
	updated.Profiles[profileKeyOne] = set.Profiles[profileKeyTwo]
	promotion := cohortProposal(t, store, updated, &initial.Activation.SelectionSet, policyregistry.ChangeProfile, profileKeyOne)
	promoted, err := controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, promotion), "workflow")
	require.NoError(t, err)
	retained := decide(&oldConversation)
	require.Equal(t, oldConversation.Selection, retained.Selection)
	newConversation := decide(nil)
	require.Equal(t, promotion.SelectionSet, newConversation.Selection.Binding)
	require.Equal(t, oldConversation.Selection.Release, newConversation.Selection.Release, "roster promotion creates no code candidate")

	// Restore just the profile configuration, independently of code rollback.
	rollback := cohortProposal(t, store, set, &promoted.Activation.SelectionSet, policyregistry.ChangeProfile, profileKeyOne)
	rolledBack, err := controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, rollback), "workflow")
	require.NoError(t, err)
	afterRollback := decide(nil)
	require.Equal(t, rollback.SelectionSet, afterRollback.Selection.Binding)
	require.Equal(t, newConversation.Selection, decide(&newConversation).Selection)
	require.Equal(t, oldConversation.Selection, decide(&oldConversation).Selection)
	cache := cohortCache(t, store)
	for _, check := range []struct {
		binding policyregistry.SessionReleaseBinding
		policy  policyregistry.PolicyObject
	}{
		{oldConversation, set.Profiles[profileKeyOne]}, {newConversation, updated.Profiles[profileKeyOne]}, {afterRollback, set.Profiles[profileKeyOne]},
	} {
		snapshot, err := cache.Snapshot(context.Background(), check.binding)
		require.NoError(t, err)
		require.Equal(t, check.policy, snapshot.Release.Policy)
	}
	require.Nil(t, rolledBack.Snapshot.State.Activations[promoted.Activation.ID].WithdrawnAt)
	// Authorization changes still rebind an otherwise retained conversation.
	projection.AssignmentGeneration++
	require.Equal(t, rolledBack.Activation.ID, decide(&newConversation).ActivationID)

	exact := cohortProposal(t, store, updated, &rolledBack.Activation.SelectionSet, policyregistry.ChangeRollback, "")
	_, err = controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, exact), "workflow")
	require.NoError(t, err, "exact historical rollback remains supported")
}

func TestCohortTargetsAndAuthenticatedWorkerScopesStayIsolated(t *testing.T) {
	store, stable := cohortFixture(t)
	stableRef := store.publishArtifact(t, policyregistry.ServingSelectionSet, stable)
	internal := stable
	internal.Target = policyregistry.TargetInternal
	internal.Profiles = maps.Clone(stable.Profiles)
	internal.Profiles[profileKeyOne] = stable.Profiles[profileKeyTwo]
	candidate := *store.object(t, policyregistry.ServingCandidate, stable.Code.Candidate).(*policyregistry.CandidateV2)
	candidate.RouterImageDigest = "sha256:" + strings.Repeat("9", 64)
	internal.Code.Candidate = store.publishArtifact(t, policyregistry.ServingCandidate, candidate)
	internal.Code.Router.Name = "internal-worker-0002"
	internal.Code.Router.ImageDigest = candidate.RouterImageDigest
	internalRef := store.publishArtifact(t, policyregistry.ServingSelectionSet, internal)
	cache := cohortCache(t, store)
	for _, check := range []struct {
		set policyregistry.SelectionSetV3
		ref policyregistry.ObjectRef
		arm string
	}{
		{stable, stableRef, "openai/gpt-5.6-sol"}, {internal, internalRef, alternateRosterArm},
	} {
		set := check.set
		binding := policyregistry.SessionReleaseBinding{Target: set.Target, ProfileKey: profileKeyOne, Selection: set.View(check.ref).Profiles[profileKeyOne]}
		snapshot, err := cache.Snapshot(context.Background(), binding)
		require.NoError(t, err)
		require.Equal(t, []string{check.arm}, snapshot.Policy.AllArms())
		worker := policyregistry.WorkerIdentity{Target: set.Target, Project: set.Code.Project, Region: set.Code.Region, Revision: set.Code.Router.Name, ImageDigest: set.Code.Router.ImageDigest, Configuration: set.Code.Router.Configuration}
		assertion := policyregistry.ServingAssertion{APIKeyID: "synthetic-key", Scope: policyregistry.AdmissionScope{InstallationID: "synthetic-installation", CredentialIdentity: "synthetic-key", Persistent: true}, Admission: binding}
		_, err = policyregistry.ValidateWorkerAdmission(context.Background(), store, worker, assertion, "synthetic-installation", "synthetic-key")
		require.NoError(t, err)
		_, err = policyregistry.ValidateWorkerAdmission(context.Background(), store, worker, assertion, "other-installation", "synthetic-key")
		require.Error(t, err)
		_, err = policyregistry.ValidateWorkerAdmission(context.Background(), store, worker, assertion, "synthetic-installation", "other-key")
		require.Error(t, err)
		worker.Target = policyregistry.TargetStaging
		_, err = policyregistry.ValidateWorkerAdmission(context.Background(), store, worker, assertion, "synthetic-installation", "synthetic-key")
		require.Error(t, err)
		firstIdentity, _ := requestcontext.ServingIdentityFromContext(policyregistry.WithServingAssertion(context.Background(), assertion))
		assertion.Scope.InstallationID = "other-installation"
		otherIdentity, _ := requestcontext.ServingIdentityFromContext(policyregistry.WithServingAssertion(context.Background(), assertion))
		require.NotEqual(t, firstIdentity.StateNamespace, otherIdentity.StateNamespace)
	}
}
