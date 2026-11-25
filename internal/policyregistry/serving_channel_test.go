package policyregistry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// targetBetaForTest stands in for the beta target, which another change adds.
const targetBetaForTest ServingTarget = "prod/beta"

func TestChannelAdmissionKeepsEnrollmentTargetForStableAndInternal(t *testing.T) {
	t.Parallel()

	beta := []ChannelPermission{{Channel: ChannelBeta, Generation: 4}}
	for _, test := range []struct {
		name        string
		worker      ServingTarget
		enrolled    ServingTarget
		permissions []ChannelPermission
	}{
		{name: "unscoped tooling", enrolled: TargetInternal},
		{name: "stable worker, default access", worker: TargetStable, enrolled: TargetStable},
		{name: "stable worker, beta holder", worker: TargetStable, enrolled: TargetStable, permissions: beta},
		{name: "stable worker, staff enrollment", worker: TargetStable, enrolled: TargetInternal},
		{name: "internal worker, staff enrollment", worker: TargetInternal, enrolled: TargetInternal, permissions: beta},
		{name: "internal worker, customer", worker: TargetInternal, enrolled: TargetStable, permissions: beta},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			channel, _ := test.worker.Channel()
			resolution, err := ChannelAdmission{Worker: test.worker, WorkerChannel: channel, Enrolled: test.enrolled, Permissions: test.permissions}.ResolveTarget()
			require.NoError(t, err)
			assert.Equal(t, ChannelResolution{Target: test.enrolled}, resolution, "a permission row must never move stable or internal admission")
		})
	}
}

func TestChannelAdmissionBetaFailsClosedWithoutItsPermission(t *testing.T) {
	t.Parallel()

	for _, permissions := range [][]ChannelPermission{nil, {{Channel: ChannelStable, Generation: 1}}} {
		_, err := ChannelAdmission{Worker: targetBetaForTest, WorkerChannel: ChannelBeta, Enrolled: TargetInternal, Permissions: permissions}.ResolveTarget()
		require.ErrorIs(t, err, ErrServingChannelNotPermitted, "internal enrollment must not grant beta")
	}
	resolution, err := ChannelAdmission{Worker: targetBetaForTest, WorkerChannel: ChannelBeta, Enrolled: TargetStable, Permissions: []ChannelPermission{{Channel: ChannelBeta, Generation: 3}}}.ResolveTarget()
	require.NoError(t, err)
	assert.Equal(t, ChannelResolution{Target: targetBetaForTest, Generation: 3}, resolution)
}

func TestRejectSessionChannelChangeOnlyAcrossHostDecidedTargets(t *testing.T) {
	t.Parallel()

	require.NoError(t, RejectSessionChannelChange(nil, targetBetaForTest))
	require.NoError(t, RejectSessionChannelChange(&SessionReleaseBinding{Target: TargetStable}, TargetInternal), "an enrollment flip still rebinds")
	require.NoError(t, RejectSessionChannelChange(&SessionReleaseBinding{Target: targetBetaForTest}, targetBetaForTest))
	require.ErrorIs(t, RejectSessionChannelChange(&SessionReleaseBinding{Target: TargetStable}, targetBetaForTest), ErrServingChannelChanged)
	require.ErrorIs(t, RejectSessionChannelChange(&SessionReleaseBinding{Target: targetBetaForTest}, TargetStable), ErrServingChannelChanged)
}

func TestParseServingChannelAcceptsOnlyKnownChannels(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"stable", "beta", "internal"} {
		channel, err := ParseServingChannel(value)
		require.NoError(t, err)
		assert.Equal(t, ServingChannel(value), channel)
	}
	_, err := ParseServingChannel("hmm_beta")
	require.Error(t, err, "a strategy name is not a channel")
}

func TestExperimentalRoutingModelsStayInternalOnly(t *testing.T) {
	t.Parallel()

	assert.False(t, ExperimentalRoutingModelsPermitted(targetBetaForTest, ""))
	assert.False(t, ExperimentalRoutingModelsPermitted(TargetStable, ""))
	assert.True(t, ExperimentalRoutingModelsPermitted(TargetInternal, ""))
}
