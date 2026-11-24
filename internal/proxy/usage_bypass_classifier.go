package proxy

import (
	"context"
	"log/slog"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
)

func (s *Service) classifierPassthroughEngaged(ctx context.Context, headers http.Header, req router.Request, turnType turntype.TurnType) (string, bool) {
	if !s.supportsSubscriptionTransport(providers.ProviderAnthropic) {
		return "", false
	}
	if turnType != turntype.Classifier {
		return "", false
	}
	provider, token, covered := subscriptionCoveredTarget(ctx, headers, req)
	if !covered || provider != providers.ProviderAnthropic || s.credentials.SubscriptionModels.Denied([]byte(token), req.RequestedModel, s.clock.Now()) {
		return "", false
	}
	if s.credentials.UsageObserver == nil {
		return provider, true
	}
	snap, observed := s.credentials.UsageObserver.Snapshot(s.credentials.UsageObserver.Key([]byte(token)))
	if observed && claudeSnapshotUnservable(ctx, snap) {
		return "", false
	}
	return provider, true
}

func bufferNonStreamBypassResponse(w http.ResponseWriter, stream bool, log *slog.Logger) (http.ResponseWriter, func()) {
	if stream {
		return w, func() {}
	}
	responseBuffer := newResponseCostBuffer(w)
	return responseBuffer, func() {
		if flushErr := responseBuffer.FlushToClient(); flushErr != nil {
			log.Error("Failed to flush buffered response", "err", flushErr)
		}
	}
}
