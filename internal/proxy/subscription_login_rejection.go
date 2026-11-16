package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

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

func includedLaneVerdict(ctx context.Context) error {
	if billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx) {
		return subscriptionPoolExhausted(ctx)
	}
	return nil
}

func flushIncludedLaneVerdict(in failoverInputs, err error) error {
	if !isSubscriptionPoolError(err) && in.flushErr != nil && !in.deferFlushOnExhaustion {
		in.flushErr(in.w, err)
	}
	return err
}
