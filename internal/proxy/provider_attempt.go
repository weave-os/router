package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

type leasedProviderAttempt struct {
	inputs   dispatch.FailoverInputs
	decision router.Decision
	target   inference.Target
	lease    subscriptions.Lease
	client   providers.Client
	managed  bool
}

func invokeLeasedProviderAttempt(service *Service, ctx context.Context, plan inference.ResolvedPlan, attempt leasedProviderAttempt) (time.Time, error) {
	started := service.clock.Now()
	observability.FromContext(ctx).Debug("Invoking leased provider attempt", "purpose", plan.Purpose(), "target", attempt.target.CatalogID, "subscription_account_id", attempt.lease.AccountID, "managed", attempt.managed)
	err := attempt.inputs.Attempt(ctx, attempt.decision, dispatch.NewUpstream(attempt.client))
	if err == nil && !attempt.managed {
		service.credentials.SubscriptionModels.MarkServedForCaller(CredentialsFromContext(ctx), attempt.decision.Provider, attempt.decision.Model, service.clock.Now())
	}
	return started, err
}
