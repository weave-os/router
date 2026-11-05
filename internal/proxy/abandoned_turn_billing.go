package proxy

import (
	"context"
	"errors"
)

// turnUsageBillable reports whether a turn's upstream usage is debited. A
// completed turn always is. A turn the client abandoned is when the upstream
// already reported usage: the provider bills what it generated before the
// disconnect. Cancellation is checked ahead of any status because a committed
// stream the client hung up on is rendered as a synthetic 502.
func turnUsageBillable(ctx context.Context, proxyErr error, in, out, cacheCreation, cacheRead int) bool {
	if proxyErr == nil {
		return true
	}
	clientGone := errors.Is(ctx.Err(), context.Canceled) && errors.Is(proxyErr, context.Canceled)
	return clientGone && in+out+cacheCreation+cacheRead > 0
}
