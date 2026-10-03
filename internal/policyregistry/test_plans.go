package policyregistry

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/subscriptions/entitlement"
)

// TestPlan selects request behavior without granting a subscriber entitlement.
type TestPlan string

const (
	TestPlanStable         TestPlan = "stable"
	TestPlanMax            TestPlan = "max"
	TestPlanBoost          TestPlan = "boost"
	TestPlanGrantHeader             = "X-Weave-Test-Grant"
	TestPlanSessionHeader           = "X-Weave-Test-Session"
	TestPlanLaunchLifetime          = time.Hour
)

// ErrTestLaunchNotFound indicates a revoke request named no launch.
var ErrTestLaunchNotFound = errors.New("internal test launch not found")

// Profile resolves the server-owned serving profile for a test selector.
func (p TestPlan) Profile() (entitlement.ServingProfile, error) {
	switch p {
	case TestPlanStable:
		return entitlement.ServingProfile{}, nil
	case TestPlanMax, TestPlanBoost:
		profile, ok := entitlement.ServingProfileFor(entitlement.Plan(p))
		if ok {
			return profile, nil
		}
	}
	return entitlement.ServingProfile{}, errors.New("unsupported internal test plan")
}

// TestPlanIdentity names an eligible personal subject and its isolated prepaid budget.
type TestPlanIdentity struct {
	SubjectID            string `json:"subject_id"`
	InstallationID       string `json:"installation_id"`
	Label                string `json:"label"`
	BalanceMicros        int64  `json:"balance_usd_micros"`
	EnrollmentGeneration int64  `json:"enrollment_generation"`
}

// TestPlanPreview binds cost confirmation to an exact production serving selection.
type TestPlanPreview struct {
	Identity       TestPlanIdentity      `json:"identity"`
	Plan           TestPlan              `json:"plan"`
	Admission      SessionReleaseBinding `json:"admission"`
	PolicyRevision string                `json:"policy_revision"`
}

// TestPlanLaunch retains one preview with an immutable expiration.
type TestPlanLaunch struct {
	ID        string          `json:"id"`
	Preview   TestPlanPreview `json:"preview"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// TestPlanConfiguration carries the bearer grant only to the session launcher.
type TestPlanConfiguration struct {
	Launch TestPlanLaunch `json:"launch"`
	Grant  string         `json:"grant"`
}

// TestPlanScope is carried only inside a versioned, gateway-signed assertion.
type TestPlanScope struct {
	Plan           TestPlan  `json:"plan"`
	SubjectID      string    `json:"subject_id"`
	LaunchID       string    `json:"launch_id"`
	SessionID      string    `json:"session_id"`
	PolicyRevision string    `json:"policy_revision"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// TestPlanRepository owns primary eligibility and expiring, single-session grants.
type TestPlanRepository interface {
	ListTestIdentities(context.Context) ([]TestPlanIdentity, error)
	GetTestIdentity(context.Context, string) (TestPlanIdentity, error)
	SaveTestLaunch(context.Context, TestPlanLaunch, string) error
	AuthorizeTestLaunch(context.Context, string, string, string, string) (TestPlanLaunch, error)
	RevokeTestLaunch(context.Context, string) error
}

// TestPlanTools prepares and admits production tests independently of subscriber state.
type TestPlanTools struct {
	Repository TestPlanRepository
	Store      ServingStore
	Clock      func() time.Time
}

// Preview resolves prod/stable after checking current internal identity and funding.
func (s *TestPlanTools) Preview(ctx context.Context, subjectID string, plan TestPlan) (TestPlanPreview, error) {
	identity, err := s.Repository.GetTestIdentity(ctx, subjectID)
	if err != nil {
		return TestPlanPreview{}, err
	}
	profile, err := plan.Profile()
	if err != nil {
		return TestPlanPreview{}, err
	}
	// Test selection never projects a subscriber entitlement or internal lane.
	admission, err := (ServingAdmission{Store: s.Store}).Decide(ctx, SerializedAdmission{
		Projection: AdmissionProjection{Target: TargetStable, ProfileKey: profile.Key, EnrollmentGeneration: identity.EnrollmentGeneration},
		Clock:      func(context.Context) (time.Time, error) { return s.Clock().UTC(), nil },
	})
	if err != nil {
		return TestPlanPreview{}, err
	}
	prepared, err := ReadPreparedSelection(ctx, s.Store, TargetStable, profile.Key, admission.Selection)
	if err != nil {
		return TestPlanPreview{}, err
	}
	return TestPlanPreview{Identity: identity, Plan: plan, Admission: admission, PolicyRevision: prepared.PolicyReference.SHA256}, nil
}

// Prepare rechecks the confirmed selection and stores only the grant digest.
func (s *TestPlanTools) Prepare(ctx context.Context, expected TestPlanPreview, confirmed bool) (TestPlanConfiguration, error) {
	if !confirmed {
		return TestPlanConfiguration{}, errors.New("live inference budget confirmation is required")
	}
	preview, err := s.Preview(ctx, expected.Identity.SubjectID, expected.Plan)
	if err != nil {
		return TestPlanConfiguration{}, err
	}
	if !sameTestSelection(expected, preview) {
		return TestPlanConfiguration{}, errors.New("selection or identity changed; preview and confirm a new launch")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return TestPlanConfiguration{}, err
	}
	grant := base64.RawURLEncoding.EncodeToString(token)
	now := s.Clock().UTC()
	launch := TestPlanLaunch{ID: uuid.NewString(), Preview: preview, CreatedAt: now, ExpiresAt: now.Add(TestPlanLaunchLifetime)}
	err = s.Repository.SaveTestLaunch(ctx, launch, Digest([]byte(grant)))
	if err != nil {
		return TestPlanConfiguration{}, err
	}
	return TestPlanConfiguration{Launch: launch, Grant: grant}, nil
}

func sameTestSelection(expected, actual TestPlanPreview) bool {
	return expected.Identity.SubjectID == actual.Identity.SubjectID && expected.Identity.InstallationID == actual.Identity.InstallationID &&
		expected.Identity.EnrollmentGeneration == actual.Identity.EnrollmentGeneration && expected.Plan == actual.Plan &&
		expected.Admission.Target == actual.Admission.Target && expected.Admission.ActivationID == actual.Admission.ActivationID &&
		expected.Admission.ProfileKey == actual.Admission.ProfileKey && sameSelection(expected.Admission.Selection, actual.Admission.Selection) && expected.PolicyRevision == actual.PolicyRevision
}

// Admit verifies the grant and exact retained selection for every request.
func (s *TestPlanTools) Admit(ctx context.Context, token, installationID, keyID, sessionID string) (ServingAssertion, error) {
	if len(token) != 43 {
		return ServingAssertion{}, errors.New("invalid test launch grant")
	}
	if session, err := uuid.Parse(sessionID); err != nil || session == uuid.Nil {
		return ServingAssertion{}, errors.New("fresh test session UUID required")
	}
	launch, err := s.Repository.AuthorizeTestLaunch(ctx, Digest([]byte(token)), installationID, keyID, sessionID)
	if err != nil {
		return ServingAssertion{}, err
	}
	preview := launch.Preview
	identity, err := s.Repository.GetTestIdentity(ctx, preview.Identity.SubjectID)
	if err != nil {
		return ServingAssertion{}, err
	}
	if identity.InstallationID != installationID || identity.EnrollmentGeneration != preview.Identity.EnrollmentGeneration {
		return ServingAssertion{}, errors.New("test identity eligibility changed; prepare a new launch")
	}
	now := s.Clock().UTC()
	if now.Before(launch.CreatedAt) || !now.Before(launch.ExpiresAt) || launch.ExpiresAt.Sub(launch.CreatedAt) > TestPlanLaunchLifetime {
		return ServingAssertion{}, errors.New("test launch expired; prepare a new launch")
	}
	admission, err := s.retainedSelection(ctx, launch, now)
	if err != nil {
		return ServingAssertion{}, err
	}
	digest, persistent := ServingConversationDigest(identity.SubjectID, launch.ID+"/"+sessionID)
	return ServingAssertion{APIKeyID: keyID, Scope: AdmissionScope{InstallationID: installationID, CredentialIdentity: identity.SubjectID, ConversationDigest: digest, Persistent: persistent}, Admission: admission,
		TestPlan: &TestPlanScope{Plan: preview.Plan, SubjectID: identity.SubjectID, LaunchID: launch.ID, SessionID: sessionID, PolicyRevision: preview.PolicyRevision, ExpiresAt: launch.ExpiresAt}}, nil
}

func (s *TestPlanTools) retainedSelection(ctx context.Context, launch TestPlanLaunch, now time.Time) (SessionReleaseBinding, error) {
	admission := launch.Preview.Admission
	if admission.Target != TargetStable {
		return SessionReleaseBinding{}, errors.New("test launch must target prod/stable")
	}
	profile, err := launch.Preview.Plan.Profile()
	if err != nil || profile.Key != admission.ProfileKey || admission.Plan != "" || admission.EntitlementVersion != 0 {
		return SessionReleaseBinding{}, errors.New("test launch profile is invalid")
	}
	snapshot, err := s.Store.ReadServingState(ctx, TargetStable)
	if err != nil {
		return SessionReleaseBinding{}, err
	}
	if err := snapshot.State.Validate(s.Store.RootURI(), TargetStable); err != nil {
		return SessionReleaseBinding{}, err
	}
	activation, ok := snapshot.State.Activations[admission.ActivationID]
	if !ok || activation.WithdrawnAt != nil || now.Before(activation.ActivatedAt) ||
		(activation.SupersededAt != nil && !now.Before(activation.SupersededAt.Add(ServingRetirementLifetime))) ||
		!now.Before(admission.LastAdmittedAt.Add(ServingIdleLifetime)) {
		return SessionReleaseBinding{}, errors.New("pinned test activation is no longer eligible; prepare a new launch")
	}
	selectionSet, err := readSelectionSetView(ctx, s.Store, activation.SelectionSet)
	if err != nil {
		return SessionReleaseBinding{}, err
	}
	selection, err := selectionForActivation(activation, admission.ProfileKey, map[string]SelectionSetView{activation.SelectionSet.SHA256: selectionSet}, s.Store.RootURI(), TargetStable)
	if err != nil {
		return SessionReleaseBinding{}, err
	}
	if !sameSelection(selection, admission.Selection) {
		return SessionReleaseBinding{}, errors.New("pinned test revision changed")
	}
	prepared, err := ReadPreparedSelection(ctx, s.Store, TargetStable, profile.Key, selection)
	if err != nil {
		return SessionReleaseBinding{}, err
	}
	if prepared.PolicyReference.SHA256 != launch.Preview.PolicyRevision {
		return SessionReleaseBinding{}, errors.New("pinned test policy revision changed")
	}
	admission.LastAdmittedAt = now
	return admission, nil
}
