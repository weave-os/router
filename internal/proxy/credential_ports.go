package proxy

import (
	"context"
	"time"
)

func wireCredentialPorts(s *Service) *Service {
	s.credentials.Clock = func() time.Time { return s.clock.Now() }
	s.credentials.SubscriptionTransport = s.supportsSubscriptionTransport
	s.credentials.CallerPassthrough = func(ctx context.Context) bool {
		passthrough, _ := callerRoutingState(ctx)
		return passthrough
	}
	return s
}
