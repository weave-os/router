package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

// codexLoginRejectedMessage tells a caller whose own ChatGPT login was
// rejected how to recover. It is also the in-stream response.failed message
// when the stream already committed and the 401 can no longer be sent.
const codexLoginRejectedMessage = "Your ChatGPT (Codex) login was rejected. Sign in to Codex again or reconnect your Codex seat: " +
	SubscriptionsDashboardURL + "?reconnect=codex"

// codexLoginRejectedBody is the bare 401 for that case. It is not wrapped in a
// pool error, which would render as a retryable 429; Codex refreshes its login
// on a 401.
var codexLoginRejectedBody = []byte(`{"error":{"type":"authentication_error","code":"token_revoked","message":"` + codexLoginRejectedMessage + `"}}`)

// ownCodexLoginRejected reports whether a Codex client's own ChatGPT login is
// held as rejected (401/403) by the usage observer. Other clients may carry a
// linked Codex token without depending on it, so they keep their fallback. A
// refreshed login is a new token and is not held.
func ownCodexLoginRejected(ctx context.Context, headers http.Header, observer *usage.Observer) bool {
	if ClientIdentityFrom(ctx).ClientApp != ClientAppCodex {
		return false
	}
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
		rejection := &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized, Body: codexLoginRejectedBody}
		return attributedError{err: rejection, message: codexLoginRejectedMessage}
	}
	return nil
}

// flushIncludedLaneVerdict renders a login rejection the way dispatch renders
// an exhausted attempt; the ingress handler renders pool errors itself.
func flushIncludedLaneVerdict(in failoverInputs, err error) error {
	if !isSubscriptionPoolError(err) && in.flushErr != nil && !in.deferFlushOnExhaustion {
		in.flushErr(in.w, err)
	}
	return err
}
