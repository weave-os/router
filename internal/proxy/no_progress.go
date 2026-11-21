package proxy

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type loopBreakPins struct {
	s        *Service
	strategy router.Strategy
}

func newLoopBreakPins(s *Service, strategy router.Strategy) conversation.LoopBreakPins {
	if s.pinStore == nil {
		return nil
	}
	return loopBreakPins{s: s, strategy: strategy}
}

func (p loopBreakPins) Load(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool) {
	return routingpolicy.NewSessionPins(p.s.pinStore, p.s.clock.Now, p.strategy).Load(ctx, sessionKey, role)
}

func (p loopBreakPins) ExpireWithHistory(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, role, reason string) error {
	return p.s.expireSessionPinAndHMMHistory(ctx, p.strategy, installationID, sessionKey, role, reason)
}

func writeLoopStop(ctx context.Context, w http.ResponseWriter, env *translate.RequestEnvelope, msg string, inputTokens int) error {
	if _, active := auth.BlindExperimentFrom(ctx); active {
		msg = blindExperimentLoopStopMessage
	}
	ack := newCommandAcknowledgment(translate.CommandLoopStop)
	return writeCommandOutcome(ctx, w, env, msg, ack.outcome, inputTokens)
}
