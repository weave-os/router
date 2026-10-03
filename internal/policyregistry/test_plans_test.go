package policyregistry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/subscriptions/entitlement"
)

type testLaunchRepo struct {
	identity      policyregistry.TestPlanIdentity
	launch        policyregistry.TestPlanLaunch
	hash, session string
	revoked       bool
	missingBudget bool
}

func (r *testLaunchRepo) ListTestIdentities(context.Context) ([]policyregistry.TestPlanIdentity, error) {
	return []policyregistry.TestPlanIdentity{r.identity}, nil
}
func (r *testLaunchRepo) GetTestIdentity(context.Context, string) (policyregistry.TestPlanIdentity, error) {
	if r.missingBudget {
		return policyregistry.TestPlanIdentity{}, errors.New("missing isolated budget")
	}
	return r.identity, nil
}
func (r *testLaunchRepo) SaveTestLaunch(_ context.Context, launch policyregistry.TestPlanLaunch, hash string) error {
	r.launch = launch
	r.hash = hash
	return nil
}
func (r *testLaunchRepo) AuthorizeTestLaunch(_ context.Context, hash, installation, key, session string) (policyregistry.TestPlanLaunch, error) {
	if hash != r.hash || r.revoked || installation != r.identity.InstallationID || key != "test-key" || (r.session != "" && r.session != session) {
		return policyregistry.TestPlanLaunch{}, errors.New("unauthorized grant")
	}
	r.session = session
	return r.launch, nil
}
func (r *testLaunchRepo) RevokeTestLaunch(context.Context, string) error {
	r.revoked = true
	return nil
}

func testPlanFixture(t *testing.T) (*policyregistry.TestPlanTools, *testLaunchRepo, *servingMemoryStore, *time.Time, policyregistry.SelectionSet) {
	t.Helper()
	store, _, set := controllerFixture(t)
	release := store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
	for _, plan := range []entitlement.Plan{entitlement.PlanMax, entitlement.PlanBoost} {
		profile, _ := entitlement.ServingProfileFor(plan)
		set.Profiles[profile.Key] = registerProfileFixture(t, store, set.Default, profile.Key, release.Policy)
	}
	snapshot, _ := storedActivateFixture(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	store.states[policyregistry.TargetStable] = snapshot
	now := servingEpoch.Add(time.Minute)
	repo := &testLaunchRepo{identity: policyregistry.TestPlanIdentity{SubjectID: uuid.NewString(), InstallationID: "test-installation", Label: "Synthetic enrolled internal identity", BalanceMicros: 1000000, EnrollmentGeneration: 3}}
	return &policyregistry.TestPlanTools{Repository: repo, Store: store, Clock: func() time.Time { return now }}, repo, store, &now, set
}

func TestInternalPlanLaunchTargetsStableAndSignsExactTestScope(t *testing.T) {
	for _, plan := range []policyregistry.TestPlan{policyregistry.TestPlanStable, policyregistry.TestPlanMax, policyregistry.TestPlanBoost} {
		t.Run(string(plan), func(t *testing.T) {
			tools, repo, store, now, _ := testPlanFixture(t)
			preview, err := tools.Preview(context.Background(), repo.identity.SubjectID, plan)
			require.NoError(t, err)
			require.Equal(t, policyregistry.TargetStable, preview.Admission.Target)
			require.Empty(t, preview.Admission.Plan)
			require.Zero(t, preview.Admission.EntitlementVersion)
			switch plan {
			case policyregistry.TestPlanStable:
				require.Empty(t, preview.Admission.ProfileKey)
			case policyregistry.TestPlanMax:
				require.Equal(t, "75018458-5306-5d29-9911-ec06c5376a9f", preview.Admission.ProfileKey)
			case policyregistry.TestPlanBoost:
				require.Equal(t, "32acfab2-7d79-55c7-8d63-e8535fb66027", preview.Admission.ProfileKey)
			}
			_, err = tools.Prepare(context.Background(), preview, false)
			require.Error(t, err)
			config, err := tools.Prepare(context.Background(), preview, true)
			require.NoError(t, err)
			require.NotEqual(t, config.Grant, repo.hash)
			session := uuid.NewString()
			admitted, err := tools.Admit(context.Background(), config.Grant, repo.identity.InstallationID, "test-key", session)
			require.NoError(t, err)
			require.Equal(t, preview.Admission.ActivationID, admitted.Admission.ActivationID)
			require.Equal(t, preview.Admission.Selection, admitted.Admission.Selection)
			require.Equal(t, repo.identity.SubjectID, admitted.TestPlan.SubjectID)
			require.Equal(t, plan, admitted.TestPlan.Plan)
			signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("s", 32)), func() time.Time { return *now })
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "https://router.example/v1/messages", strings.NewReader("{}"))
			encoded, err := signer.Sign(admitted, request, []byte("{}"), "rk_test")
			require.NoError(t, err)
			verified, err := signer.Verify(encoded, request, []byte("{}"), "rk_test")
			require.NoError(t, err)
			require.Equal(t, policyregistry.ServingAssertionV2, verified.SchemaVersion)
			require.Equal(t, config.Launch.ID, verified.TestPlan.LaunchID)
			admitted.TestPlan.SubjectID = "spoofed-subscriber"
			_, err = signer.Sign(admitted, request, []byte("{}"), "rk_test")
			require.Error(t, err)
			require.Len(t, store.states, 1, "no internal target lookup or entitlement mutation")
		})
	}
}

func TestInternalPlanLaunchFailsClosedOnLifecycleIdentityAndGrantChanges(t *testing.T) {
	for _, scenario := range []string{"expired", "revoked", "identity", "budget", "session", "grant", "withdrawn", "missing activation", "missing profile"} {
		t.Run(scenario, func(t *testing.T) {
			tools, repo, store, now, originalSet := testPlanFixture(t)
			preview, err := tools.Preview(context.Background(), repo.identity.SubjectID, policyregistry.TestPlanMax)
			require.NoError(t, err)
			config, err := tools.Prepare(context.Background(), preview, true)
			require.NoError(t, err)
			session := uuid.NewString()
			_, err = tools.Admit(context.Background(), config.Grant, repo.identity.InstallationID, "test-key", session)
			require.NoError(t, err)
			switch scenario {
			case "expired":
				*now = config.Launch.ExpiresAt
			case "revoked":
				require.NoError(t, tools.Repository.RevokeTestLaunch(context.Background(), config.Launch.ID))
			case "identity":
				repo.identity.EnrollmentGeneration++
			case "budget":
				repo.missingBudget = true
			case "session":
				session = uuid.NewString()
			case "grant":
				config.Grant = strings.Repeat("x", 43)
			case "withdrawn", "missing activation":
				snapshot := store.states[policyregistry.TargetStable]
				previous := snapshot.State.Activations[preview.Admission.ActivationID]
				next, _ := storedActivateFixture(t, store, snapshot, variantSet(t, store, originalSet, "worker-new"), now.Add(time.Minute))
				if scenario == "withdrawn" {
					previous = next.State.Activations[previous.ID]
					withdrawnAt := next.State.Activations[next.State.CurrentActivationID].ActivatedAt
					previous.WithdrawnAt = &withdrawnAt
					previous.ReplacementID = next.State.CurrentActivationID
					next.State.Activations[previous.ID] = previous
				} else {
					delete(next.State.Activations, previous.ID)
				}
				store.states[policyregistry.TargetStable] = next
				*now = now.Add(2 * time.Minute)
			case "missing profile":
				snapshot := store.states[policyregistry.TargetStable]
				activation := snapshot.State.Activations[preview.Admission.ActivationID]
				delete(store.objects, activation.SelectionSet)
			}
			_, err = tools.Admit(context.Background(), config.Grant, repo.identity.InstallationID, "test-key", session)
			require.Error(t, err)
		})
	}
}

func TestInternalPlanPreparationRejectsChangedPreviewAndMissingProfile(t *testing.T) {
	tools, repo, store, _, _ := testPlanFixture(t)
	preview, err := tools.Preview(context.Background(), repo.identity.SubjectID, policyregistry.TestPlanStable)
	require.NoError(t, err)
	preview.Admission.ActivationID = uuid.NewString()
	_, err = tools.Prepare(context.Background(), preview, true)
	require.ErrorContains(t, err, "changed")
	snapshot := store.states[policyregistry.TargetStable]
	activation := snapshot.State.Activations[snapshot.State.CurrentActivationID]
	delete(store.objects, activation.SelectionSet)
	_, err = tools.Preview(context.Background(), repo.identity.SubjectID, policyregistry.TestPlanBoost)
	require.Error(t, err)
}

func TestInternalTestLaunchRetainsSelectionAfterStableHeadAdvances(t *testing.T) {
	tools, repo, store, now, original := testPlanFixture(t)
	preview, err := tools.Preview(context.Background(), repo.identity.SubjectID, policyregistry.TestPlanBoost)
	require.NoError(t, err)
	configuration, err := tools.Prepare(context.Background(), preview, true)
	require.NoError(t, err)
	next, _ := storedActivateFixture(t, store, store.states[policyregistry.TargetStable], variantSet(t, store, original, "successor-worker"), now.Add(time.Minute))
	store.states[policyregistry.TargetStable] = next
	*now = now.Add(2 * time.Minute)
	require.NotEqual(t, preview.Admission.ActivationID, next.State.CurrentActivationID)
	admitted, err := tools.Admit(context.Background(), configuration.Grant, repo.identity.InstallationID, "test-key", uuid.NewString())
	require.NoError(t, err)
	require.Equal(t, preview.Admission.ActivationID, admitted.Admission.ActivationID)
	require.Equal(t, preview.Admission.Selection, admitted.Admission.Selection)
	require.Equal(t, preview.PolicyRevision, admitted.TestPlan.PolicyRevision)
}
