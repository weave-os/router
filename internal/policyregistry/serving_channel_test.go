package policyregistry

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelAdmissionKeepsEnrollmentTargetForStableAndInternal(t *testing.T) {
	t.Parallel()

	beta := []ChannelPermission{{Channel: ChannelBeta, Generation: 4}}
	for _, test := range []struct {
		name           string
		worker         ServingTarget
		enrolledTarget ServingTarget
		permissions    []ChannelPermission
	}{
		{name: "unscoped tooling", enrolledTarget: TargetInternal},
		{name: "stable worker, default access", worker: TargetStable, enrolledTarget: TargetStable},
		{name: "stable worker, beta holder", worker: TargetStable, enrolledTarget: TargetStable, permissions: beta},
		{name: "stable worker, staff enrollment", worker: TargetStable, enrolledTarget: TargetInternal},
		{name: "internal worker, staff enrollment", worker: TargetInternal, enrolledTarget: TargetInternal, permissions: beta},
		{name: "internal worker, customer", worker: TargetInternal, enrolledTarget: TargetStable, permissions: beta},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			channel, _ := test.worker.Channel()
			resolution, err := ChannelAdmission{Worker: test.worker, WorkerChannel: channel, EnrolledTarget: test.enrolledTarget, Permissions: test.permissions}.ResolveTarget()
			require.NoError(t, err)
			assert.Equal(t, ChannelResolution{Target: test.enrolledTarget}, resolution, "a permission row must never move stable or internal admission")
		})
	}
}

func TestChannelAdmissionBetaFailsClosedWithoutItsPermission(t *testing.T) {
	t.Parallel()

	for _, permissions := range [][]ChannelPermission{nil, {{Channel: ChannelStable, Generation: 1}}} {
		_, err := ChannelAdmission{Worker: TargetBeta, WorkerChannel: ChannelBeta, EnrolledTarget: TargetInternal, Permissions: permissions}.ResolveTarget()
		require.ErrorIs(t, err, ErrServingChannelNotPermitted, "internal enrollment must not grant beta")
	}
	resolution, err := ChannelAdmission{Worker: TargetBeta, WorkerChannel: ChannelBeta, EnrolledTarget: TargetStable, Permissions: []ChannelPermission{{Channel: ChannelBeta, Generation: 3}}}.ResolveTarget()
	require.NoError(t, err)
	assert.Equal(t, ChannelResolution{Target: TargetBeta, Generation: 3}, resolution)
}

func TestRejectSessionChannelChangeOnlyAcrossHostDecidedTargets(t *testing.T) {
	t.Parallel()

	require.NoError(t, RejectSessionChannelChange(nil, TargetBeta))
	require.NoError(t, RejectSessionChannelChange(&SessionReleaseBinding{Target: TargetStable}, TargetInternal), "an enrollment flip still rebinds")
	require.NoError(t, RejectSessionChannelChange(&SessionReleaseBinding{Target: TargetBeta}, TargetBeta))
	require.ErrorIs(t, RejectSessionChannelChange(&SessionReleaseBinding{Target: TargetStable}, TargetBeta), ErrServingChannelChanged)
	require.ErrorIs(t, RejectSessionChannelChange(&SessionReleaseBinding{Target: TargetBeta}, TargetStable), ErrServingChannelChanged)
}

func TestParseServingChannelAcceptsOnlyKnownChannels(t *testing.T) {
	t.Parallel()

	channel, err := ParseServingChannel("beta")
	require.NoError(t, err)
	assert.Equal(t, ChannelBeta, channel)
	for _, value := range []string{"hmm_beta", "stable", "internal"} {
		_, err := ParseServingChannel(value)
		require.Error(t, err, "%q is not a channel a permission row can carry", value)
	}
}

func TestExperimentalRoutingModelsStayInternalOnly(t *testing.T) {
	t.Parallel()

	assert.False(t, ExperimentalRoutingModelsPermitted(TargetBeta, ""))
	assert.False(t, ExperimentalRoutingModelsPermitted(TargetStable, ""))
	assert.True(t, ExperimentalRoutingModelsPermitted(TargetInternal, ""))
}

func TestBetaTargetIsAProductionCohortOnTheBetaChannel(t *testing.T) {
	t.Parallel()

	environment, err := TargetBeta.Environment()
	require.NoError(t, err)
	assert.Equal(t, EnvironmentProd, environment)
	channel, found := TargetBeta.Channel()
	require.True(t, found, "a beta worker must scope admission to its channel")
	assert.Equal(t, ChannelBeta, channel)
	assert.False(t, EnrollmentDecidesTarget(TargetBeta), "beta admission comes from channel permission, not enrollment")
	require.NoError(t, WorkerIdentity{
		Target: TargetBeta, Project: "prod-01", Region: "us-central1", Revision: "router-worker-beta-00001",
		ImageDigest: "sha256:" + strings.Repeat("a", 64), Configuration: ObjectRef{URI: "gs://registry-example/configuration.json", SHA256: strings.Repeat("b", 64), Generation: 1},
	}.Validate(), "worker startup accepts the beta target")
}
