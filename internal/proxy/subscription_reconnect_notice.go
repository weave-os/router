package proxy

import (
	"context"
	"errors"
	"net/http"
	"time"

	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
)

const claudeRejectedTokenHold = 30 * time.Minute

const ownCodexCredentialRejectedNudge = routingMarkerPrefix +
	"your ChatGPT (Codex) login was rejected. Reconnect your Codex seat: " +
	SubscriptionsDashboardURL + "?reconnect=codex\n\n"

func codexSubscriptionReconnectNudge(ctx context.Context, headers http.Header, observer *usage.Observer) string {
	token, _ := presentSubscriptionTokens(ctx, headers)
	if observer.Rejected(observer.Key([]byte(token))) {
		return ownCodexCredentialRejectedNudge
	}
	return ownCodexPlanSpentNudge
}

func withOwnCodexRejectionNudge(ctx context.Context, headers http.Header, observer *usage.Observer) context.Context {
	if !ownCodexLoginRejected(ctx, headers, observer) {
		return ctx
	}
	return withSubscriptionReconnectNudge(ctx, ownCodexCredentialRejectedNudge)
}

const ownClaudeCredentialRejectedNudge = routingMarkerPrefix +
	"your Claude login was rejected. Reconnect your Claude seat: " +
	SubscriptionsDashboardURL + "?reconnect=claude\n\n"

func ownClaudeLoginRejected(ctx context.Context, headers http.Header, observer *usage.Observer) bool {
	_, token := presentSubscriptionTokens(ctx, headers)
	return token != "" && observer != nil && observer.Rejected(observer.Key([]byte(token)))
}

func claudeSubscriptionReconnectNudge(ctx context.Context, headers http.Header, observer *usage.Observer) string {
	if ownClaudeLoginRejected(ctx, headers, observer) {
		return ownClaudeCredentialRejectedNudge
	}
	return ownClaudePlanSpentNudge
}

func withOwnClaudeRejectionNudge(ctx context.Context, headers http.Header, observer *usage.Observer) context.Context {
	if !ownClaudeLoginRejected(ctx, headers, observer) {
		return ctx
	}
	return withSubscriptionReconnectNudge(ctx, ownClaudeCredentialRejectedNudge)
}

func recordOwnClaudeLoginRejected(ctx context.Context, headers http.Header, observer *usage.Observer, now time.Time, err error) {
	var upstream *providers.UpstreamErrorResponse
	if observer == nil || !anthropicOAuthCredentialRejected(err) || !errors.As(err, &upstream) || upstream.Status != http.StatusUnauthorized {
		return
	}
	_, token := presentSubscriptionTokens(ctx, headers)
	creds := CredentialsFromContext(ctx)
	if token == "" || creds == nil || !creds.OAuth || string(creds.APIKey) != token {
		return
	}
	observer.RecordRejected(observer.Key(creds.APIKey), now.Add(claudeRejectedTokenHold))
	observability.FromContext(ctx).Info("Claude subscription token rejected; skipping it on later turns", "hold", claudeRejectedTokenHold)
}
