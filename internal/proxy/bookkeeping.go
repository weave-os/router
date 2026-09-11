package proxy

import (
	"context"
	"time"
)

const bookkeepingTimeout = 250 * time.Millisecond

type bookkeepingContextKey struct{}

// Bookkeeping survives client disconnects; nested writes share its deadline.
func bookkeepingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if bounded, _ := ctx.Value(bookkeepingContextKey{}).(bool); bounded {
		return context.WithCancel(ctx)
	}
	ctx = context.WithValue(context.WithoutCancel(ctx), bookkeepingContextKey{}, true)
	return context.WithTimeout(ctx, bookkeepingTimeout)
}
