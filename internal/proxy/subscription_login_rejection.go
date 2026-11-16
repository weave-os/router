package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

const codexLoginRejectedMessage = "Your ChatGPT (Codex) login was rejected. Sign in to Codex again or reconnect your Codex seat: " +
	SubscriptionsDashboardURL + "?reconnect=codex"

var codexLoginRejectedBody = []byte(`{"error":{"type":"authentication_error","code":"token_revoked","message":"` + codexLoginRejectedMessage + `"}}`)

func ownCodexLoginRejected(ctx context.Context, headers http.Header, observer *usage.Observer) bool {
	if ClientIdentityFrom(ctx).ClientApp != ClientAppCodex {
		return false
	}
	token, _ := presentSubscriptionTokens(ctx, headers)
	return token != "" && observer != nil && observer.Rejected(observer.Key([]byte(token)))
}

func includedFailureAfter(prev, err error, ownLoginRejected bool) error {
	switch {
	case codexOAuthCredentialRejected(prev):
		return prev
	case ownLoginRejected && codexOAuthCredentialRejected(err), providers.IsUpstreamRateLimited(err):
		return err
	}
	return prev
}

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

func flushIncludedLaneVerdict(in failoverInputs, err error) error {
	if !isSubscriptionPoolError(err) && in.flushErr != nil && !in.deferFlushOnExhaustion {
		in.flushErr(in.w, err)
	}
	return err
}
