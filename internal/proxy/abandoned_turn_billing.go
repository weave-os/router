package proxy

import (
	"context"
	"errors"
	"weave-os/router/internal/dispatch"
)

func turnUsageBillable(ctx context.Context, proxyErr error, in, out, cacheCreation, cacheRead int) bool {
	if proxyErr == nil {
		return true
	}
	return clientAbandonedTurn(ctx, proxyErr) && in+out+cacheCreation+cacheRead > 0
}

func clientAbandonedTurn(ctx context.Context, err error) bool {
	if !errors.Is(ctx.Err(), context.Canceled) || isUpstreamWatchdogError(context.Cause(ctx)) {
		return false
	}
	err = dispatch.UnwrapSyntheticStatusErrors(err)
	return !isUpstreamWatchdogError(err) && upstreamStatus(err) == 0
}
