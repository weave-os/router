package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var strikeTestNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func strikeRescuedPrimary(svc *Service, ctx context.Context, err error) (string, sessionpin.DemotionReason) {
	return svc.maybeStrikeArmAfterRescuedFailure(
		ctx,
		true,
		false,
		err,
		rescuedPrimaryDecision("hmm:authoritative model=claude-opus-4-7"),
		uuid.New(),
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)
}

func strikeTurnLoopService(store sessionpin.Store) (*Service, *authoritativeTestRouter) {
	scorer := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    freshTurnModel,
		Reason:   "cluster:v0.2",
	}}
	svc := NewService(scorer, nil, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	svc.now = func() time.Time { return strikeTestNow }
	return svc, scorer
}

// A rescued 429 is a session-lifetime strike on both rows the next turn merges.
func TestRescued429_DemotesForSession(t *testing.T) {
	store := &demotionStubPinStore{}
	svc := newRescuedDemotionTestService(store, true)

	model, reason := strikeRescuedPrimary(svc, context.Background(), &providers.UpstreamErrorResponse{Status: 429})

	require.Equal(t, demotedArm, model)
	assert.Equal(t, sessionpin.DemotionReasonRescuedFailure, reason)
	assert.Equal(t, []demotionCall{
		{role: sessionpin.DefaultRole, model: demotedArm, reason: sessionpin.DemotionReasonRescuedFailure},
		{role: hmmHistoryRole(sessionpin.DefaultRole), model: demotedArm, reason: sessionpin.DemotionReasonRescuedFailure},
	}, store.demotions)
}
