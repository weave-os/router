package proxy

import (
	"context"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

type continuationPins struct {
	store       sessionpin.Store
	sessionPins routingpolicy.SessionPins
}

func (p continuationPins) Load(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool) {
	return p.sessionPins.Load(ctx, sessionKey, role)
}

func (p continuationPins) Upsert(ctx context.Context, pin sessionpin.Pin) {
	p.sessionPins.Upsert(ctx, pin)
}

func (p continuationPins) Consume(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, strategy router.Strategy) (sessionpin.Pin, bool, error) {
	return p.store.Consume(ctx, sessionKey, role, strategy)
}

func postCommandContinuation(s *Service, strategy router.Strategy) conversation.PostCommandContinuation {
	continuation := conversation.PostCommandContinuation{Strategy: strategy}
	if s.pinStore != nil {
		continuation.Now = s.clock.Now
		continuation.Pins = continuationPins{store: s.pinStore, sessionPins: routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy)}
	}
	return continuation
}
