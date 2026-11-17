package dispatch

import (
	"context"
	"weave-os/router/internal/requestcontext"
)

func ProtocolRepairAllowed(ctx context.Context) bool {
	return !requestcontext.IncludedSubscriptionOnlyFrom(ctx)
}
