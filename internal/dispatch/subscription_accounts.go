package dispatch

import (
	"context"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

type SubscriptionAccountPool interface {
	Begin(ctx context.Context, initial router.Decision) (context.Context, SubscriptionAccounts)
}

type SubscriptionAccounts interface {
	Serve(ctx, attemptCtx context.Context, attempt AccountAttempt) error
	Bound() bool
}

type AccountAttempt struct {
	Inputs  FailoverInputs
	Plan    inference.ResolvedPlan
	Attempt Attempt
	Client  providers.Client
}
