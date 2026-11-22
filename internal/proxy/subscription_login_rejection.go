package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

const codexLoginRejectedMessage = "Your ChatGPT (Codex) login was rejected. Sign in to Codex again or reconnect your Codex seat: " +
	SubscriptionsDashboardURL + "?reconnect=codex"

var codexLoginRejectedBody = []byte(`{"error":{"type":"authentication_error","code":"token_revoked","message":"` + codexLoginRejectedMessage + `"}}`)

func attributeCodexLoginRejection(err error) error {
	if !codexOAuthCredentialRejected(err) {
		return err
	}
	rejection := &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized, Body: codexLoginRejectedBody}
	return attributedError{err: ownLoginRejection{err: rejection}, message: codexLoginRejectedMessage}
}

type ownLoginRejection struct{ err error }

func (e ownLoginRejection) Error() string { return e.err.Error() }
func (e ownLoginRejection) Unwrap() error { return e.err }

func isOwnLoginRejection(err error) bool {
	var rejection ownLoginRejection
	return errors.As(err, &rejection)
}

func ownCodexLoginRejected(ctx context.Context, headers http.Header, observer *usage.Observer) bool {
	if ClientIdentityFrom(ctx).ClientApp != requestcontext.ClientAppCodex {
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

func includedLaneVerdict(ctx context.Context) error {
	if billing.SubscriptionOnlyFromContext(ctx) && !credentials.LinkedFirst(ctx) {
		return credentials.SubscriptionPoolExhausted(ctx)
	}
	return nil
}

func flushIncludedLaneVerdict(in dispatch.FailoverInputs, err error) error {
	if !isSubscriptionPoolError(err) && in.FlushErr != nil && !in.DeferFlushOnExhaustion {
		in.FlushErr(in.Writer, err)
	}
	return err
}
