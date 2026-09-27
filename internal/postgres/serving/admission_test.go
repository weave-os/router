package serving

import (
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriberPlanProjectionUsesServerOwnedProfile(t *testing.T) {
	t.Parallel()

	for _, plan := range []entitlement.Plan{entitlement.PlanMax, entitlement.PlanBoost} {
		t.Run(string(plan), func(t *testing.T) {
			profile, ok := entitlement.ServingProfileFor(plan)
			require.True(t, ok)

			projection, err := subscriberPlanProjection(string(plan), 7)
			require.NoError(t, err)
			assert.Equal(t, profile.Key, projection.ProfileKey)
			assert.Equal(t, profile.Name, projection.ProfileName)
			assert.Equal(t, plan, projection.Plan)
			assert.Equal(t, int64(7), projection.EntitlementVersion)
			assert.Equal(t, int64(7), projection.AssignmentGeneration)
		})
	}
}

func TestSubscriberPlanProjectionRejectsUnknownOrUnversionedPlan(t *testing.T) {
	t.Parallel()

	_, err := subscriberPlanProjection("unknown", 1)
	require.Error(t, err)
	_, err = subscriberPlanProjection(string(entitlement.PlanMax), 0)
	require.Error(t, err)
}

func TestEnrollmentProjectionRoutesEnrolledInstallationsAndSubjectsInternally(t *testing.T) {
	t.Parallel()

	enrolledLane := sqlc.GetServingInstallationLaneEnrollmentRow{InternalEnrolled: true, EnrollmentGeneration: 3}
	disenrolledLane := sqlc.GetServingInstallationLaneEnrollmentRow{EnrollmentGeneration: 4}
	enrolledSubject := &auth.CredentialSubject{ID: "subject", InternalEnrolled: true, EnrollmentGeneration: 5}
	stableSubject := &auth.CredentialSubject{ID: "subject", EnrollmentGeneration: 6}

	assert.Equal(t, policyregistry.AdmissionProjection{Target: policyregistry.TargetStable}, enrollmentProjection(sqlc.GetServingInstallationLaneEnrollmentRow{}, nil))
	assert.Equal(t, policyregistry.AdmissionProjection{Target: policyregistry.TargetInternal, EnrollmentGeneration: 3}, enrollmentProjection(enrolledLane, nil))
	assert.Equal(t, policyregistry.AdmissionProjection{Target: policyregistry.TargetInternal, EnrollmentGeneration: 5}, enrollmentProjection(sqlc.GetServingInstallationLaneEnrollmentRow{}, enrolledSubject))
	assert.Equal(t, policyregistry.AdmissionProjection{Target: policyregistry.TargetInternal, EnrollmentGeneration: 9}, enrollmentProjection(enrolledLane, stableSubject))
	assert.Equal(t, policyregistry.AdmissionProjection{Target: policyregistry.TargetStable, EnrollmentGeneration: 10}, enrollmentProjection(disenrolledLane, stableSubject))
}

func TestEnrollmentProjectionGenerationChangesWhenEitherEnrollmentFlips(t *testing.T) {
	t.Parallel()

	subject := &auth.CredentialSubject{ID: "subject", EnrollmentGeneration: 2}
	before := enrollmentProjection(sqlc.GetServingInstallationLaneEnrollmentRow{EnrollmentGeneration: 1}, subject)
	laneFlipped := enrollmentProjection(sqlc.GetServingInstallationLaneEnrollmentRow{InternalEnrolled: true, EnrollmentGeneration: 2}, subject)
	subjectFlipped := enrollmentProjection(sqlc.GetServingInstallationLaneEnrollmentRow{EnrollmentGeneration: 1}, &auth.CredentialSubject{ID: "subject", InternalEnrolled: true, EnrollmentGeneration: 3})

	assert.NotEqual(t, before.EnrollmentGeneration, laneFlipped.EnrollmentGeneration)
	assert.NotEqual(t, before.EnrollmentGeneration, subjectFlipped.EnrollmentGeneration)
	assert.Equal(t, policyregistry.TargetInternal, laneFlipped.Target)
	assert.Equal(t, policyregistry.TargetInternal, subjectFlipped.Target)
}
