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

	for _, tc := range []struct {
		plan        entitlement.Plan
		profileKey  string
		profileName string
	}{
		{plan: entitlement.PlanMax, profileKey: "75018458-5306-5d29-9911-ec06c5376a9f", profileName: "max-default"},
		{plan: entitlement.PlanBoost},
	} {
		t.Run(string(tc.plan), func(t *testing.T) {
			projection, err := subscriberPlanProjection(string(tc.plan), 7)
			require.NoError(t, err)
			assert.Equal(t, tc.profileKey, projection.ProfileKey)
			assert.Equal(t, tc.profileName, projection.ProfileName)
			assert.Equal(t, tc.plan, projection.Plan)
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

func TestChannelPermissionsRejectUnknownChannel(t *testing.T) {
	t.Parallel()

	permissions, err := channelPermissions([]sqlc.ListServingInstallationChannelPermissionsRow{{Channel: "beta", Generation: 2}})
	require.NoError(t, err)
	assert.Equal(t, []policyregistry.ChannelPermission{{Channel: policyregistry.ChannelBeta, Generation: 2}}, permissions)
	_, err = channelPermissions([]sqlc.ListServingInstallationChannelPermissionsRow{{Channel: "hmm_beta", Generation: 1}})
	require.Error(t, err)
}

func TestAdmissionRefusalsAreDeniedNotFailed(t *testing.T) {
	t.Parallel()

	assert.Equal(t, admissionDenied, admissionOutcomeFor(policyregistry.ErrServingChannelNotPermitted))
	assert.Equal(t, admissionDenied, admissionOutcomeFor(policyregistry.ErrServingChannelChanged))
}

func TestServingAdmissionRepoRefusesAWorkerWithoutIdentityOrChannel(t *testing.T) {
	t.Parallel()

	unscoped := &ServingAdmissionRepo{environment: policyregistry.EnvironmentProd}
	for _, test := range []struct {
		name    string
		worker  policyregistry.ServingTarget
		channel policyregistry.ServingChannel
	}{
		{name: "no worker identity"},
		{name: "beta worker without its channel", worker: policyregistry.TargetBeta},
		{name: "beta worker on another channel", worker: policyregistry.TargetBeta, channel: policyregistry.ChannelStable},
	} {
		_, err := unscoped.ForWorker(test.worker, test.channel)
		require.ErrorIs(t, err, policyregistry.ErrServingWorkerUnscoped, test.name)
	}

	for _, worker := range []policyregistry.ServingTarget{policyregistry.TargetStable, policyregistry.TargetInternal, policyregistry.TargetBeta, policyregistry.TargetStaging} {
		channel, _ := worker.Channel()
		_, err := unscoped.ForWorker(worker, channel)
		require.NoError(t, err, worker)
	}
}
