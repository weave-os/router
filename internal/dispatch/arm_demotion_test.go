package dispatch_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/sessionpin"
)

type recordingDemoter struct {
	reasons   []sessionpin.DemotionReason
	cooldowns []time.Time
}

func (d *recordingDemoter) DemoteArm(_ context.Context, _ string, reason sessionpin.DemotionReason, cooldownUntil time.Time, _ int) bool {
	d.reasons = append(d.reasons, reason)
	d.cooldowns = append(d.cooldowns, cooldownUntil)
	return true
}

func TestArmDemotionCommittedStreamFailureDemotesWhenEnabled(t *testing.T) {
	demoter := &recordingDemoter{}
	failure := dispatch.ArmFailure{Session: demoter, Err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}, Model: "model-a"}

	assert.Empty(t, dispatch.ArmDemotionPolicy{}.AfterCommittedStreamFailure(context.Background(), true, failure))
	assert.Equal(t, "model-a", dispatch.ArmDemotionPolicy{CommittedStream: true}.AfterCommittedStreamFailure(context.Background(), true, failure))
	assert.Equal(t, []sessionpin.DemotionReason{sessionpin.DemotionReasonCommittedStreamFailure}, demoter.reasons)
}

func TestArmDemotionSkipsWithoutSession(t *testing.T) {
	failure := dispatch.ArmFailure{Err: providers.ErrUpstreamOutputStall, Model: "model-a"}
	policy := dispatch.ArmDemotionPolicy{CommittedStream: true, RescuedFailure: true}

	assert.Empty(t, policy.AfterCommittedStreamFailure(context.Background(), true, failure))
	assert.Empty(t, policy.AfterUnrescuedStall(context.Background(), false, false, failure))
}

func TestArmDemotionRescuedRateLimitCoolsDown(t *testing.T) {
	demoter := &recordingDemoter{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	model, reason := dispatch.ArmDemotionPolicy{RescuedFailure: true}.StrikeAfterRescuedFailure(context.Background(), dispatch.RescuedFailureStrike{
		ArmFailure:            dispatch.ArmFailure{Session: demoter, Err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}, Model: "model-a"},
		RescueRan:             true,
		RescuedPrimaryFailure: true,
		RateLimit:             dispatch.NewRateLimitPolicy(true, 30),
		Now:                   func() time.Time { return now },
	})

	assert.Equal(t, "model-a", model)
	assert.Equal(t, sessionpin.DemotionReasonRateLimited, reason)
	assert.Equal(t, []time.Time{now.Add(30 * time.Second)}, demoter.cooldowns)
}

func TestArmDemotionRescuedFailureRecordsTurnFailureWhenDisabled(t *testing.T) {
	var recorded []string
	model, _ := dispatch.ArmDemotionPolicy{}.StrikeAfterRescuedFailure(context.Background(), dispatch.RescuedFailureStrike{
		ArmFailure:            dispatch.ArmFailure{Session: &recordingDemoter{}, Err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}, Model: "model-a"},
		RescueRan:             true,
		RescuedPrimaryFailure: true,
		RecordTurnFailure:     func(_ context.Context, model string) { recorded = append(recorded, model) },
	})

	assert.Empty(t, model)
	assert.Equal(t, []string{"model-a"}, recorded)
}
