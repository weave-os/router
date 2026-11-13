package proxy

import (
	"context"
	"errors"
)

// turnUsageBillable reports whether a turn's upstream usage is debited. A
// completed turn always is; one the client abandoned is when the upstream
// already reported usage, since the provider bills what it generated.
func turnUsageBillable(ctx context.Context, proxyErr error, in, out, cacheCreation, cacheRead int) bool {
	if proxyErr == nil {
		return true
	}
	return clientAbandonedTurn(ctx, proxyErr) && in+out+cacheCreation+cacheRead > 0
}

// clientAbandonedTurn reports whether err ended the turn because the client
// went away. The inbound context is the signal: a downstream write failure
// surfaces as a bare broken pipe or reset. A watchdog abort cancels with an
// upstream sentinel as the cause, and a real upstream status is the
// provider's failure, so neither counts.
func clientAbandonedTurn(ctx context.Context, err error) bool {
	if !errors.Is(ctx.Err(), context.Canceled) || isUpstreamWatchdogError(context.Cause(ctx)) {
		return false
	}
	err = dispatchErrorBehindFrames(err)
	return !isUpstreamWatchdogError(err) && upstreamStatus(err) == 0
}
