package proxy

import (
	"context"
	"time"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

type leasedProviderAttempt struct {
	inputs   failoverInputs
	decision router.Decision
	target   inference.Target
	lease    subscriptions.Lease
	client   providers.Client
	managed  bool
}

func invokeLeasedProviderAttempt(service *Service, ctx context.Context, plan inference.ResolvedPlan, attempt leasedProviderAttempt) (time.Time, error) {
	started := service.clock.Now()
	observability.FromContext(ctx).Debug("Invoking leased provider attempt", "purpose", plan.Purpose(), "target", attempt.target.CatalogID, "subscription_account_id", attempt.lease.AccountID, "managed", attempt.managed)
	return started, attemptWithUsageObservation(ctx, service.usageObserver, attempt.inputs.attempt, attempt.decision, attempt.client, service.clock)
}
