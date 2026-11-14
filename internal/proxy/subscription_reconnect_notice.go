package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/proxy/usage"
)

const ownCodexCredentialRejectedNudge = routingMarkerPrefix +
	"your ChatGPT (Codex) login was rejected. Reconnect your Codex seat: " +
	SubscriptionsDashboardURL + "?reconnect=codex\n\n"

func codexSubscriptionReconnectNudge(ctx context.Context, headers http.Header, observer *usage.Observer) string {
	if ownCodexLoginRejected(ctx, headers, observer) {
		return ownCodexCredentialRejectedNudge
	}
	return ownCodexPlanSpentNudge
}
