package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

// codexLoginRejectedBody is the bare 401 a turn answers with when the caller's
// own ChatGPT login was rejected. It is not wrapped in a pool error, which
// would render as a retryable 429; Codex refreshes its login on a 401.
var codexLoginRejectedBody = []byte(`{"error":{"type":"authentication_error","code":"token_revoked","message":"Your ChatGPT (Codex) login was rejected. Sign in to Codex again or reconnect your Codex seat: ` +
	SubscriptionsDashboardURL + `?reconnect=codex"}}`)

// ownCodexLoginRejected reports whether the Codex login the caller presented
// is held as rejected (401/403) by the usage observer. A refreshed login is a
// new token and is not held.
func ownCodexLoginRejected(ctx context.Context, headers http.Header, observer *usage.Observer) bool {
	token, _ := presentSubscriptionTokens(ctx, headers)
	return token != "" && observer != nil && observer.Rejected(observer.Key([]byte(token)))
}

// includedFailureAfter keeps the included-lane failure the turn reports when
// no included target serves it. A rejected own Codex login outranks a rate
// limit: the client must sign in again before anything else helps.
func includedFailureAfter(prev, err error, ownLoginRejected bool) error {
	switch {
	case codexOAuthCredentialRejected(prev):
		return prev
	case ownLoginRejected && codexOAuthCredentialRejected(err), providers.IsUpstreamRateLimited(err):
		return err
	}
	return prev
}

// includedLaneVerdict ends the turn when the included lane may not hand it to
// the exhausted model set. A revoked own Codex login is not spent quota: the
// caller must sign in again, so the turn fails with a 401 instead of
// silently serving the session from allowance models.
func includedLaneVerdict(ctx context.Context, includedFailure error, ownLoginRejected bool) error {
	switch {
	case billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx):
		return subscriptionPoolExhausted(ctx)
	case codexOAuthCredentialRejected(includedFailure), ownLoginRejected:
		return &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized, Body: codexLoginRejectedBody}
	}
	return nil
}
