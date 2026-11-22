package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/subscriptionlane"
)

type paidFallbackObservation struct {
	observed bool
	on       subscriptionlane.BindingEvent
}

func stepPaidFallbackBinding(observations ...paidFallbackObservation) subscriptionlane.Binding {
	binding := subscriptionlane.Unbound
	for _, observation := range observations {
		if observation.observed {
			binding, _ = subscriptionlane.StepBinding(binding, observation.on)
		}
	}
	return binding
}

func paidFallbackBinding(ctx context.Context) subscriptionlane.Binding {
	enterpriseSeat := claudeEnterpriseTurn(ctx)
	return stepPaidFallbackBinding(
		paidFallbackObservation{subscriptionStateModelsEnabled(ctx), subscriptionlane.IncludedLaneEntered},
		paidFallbackObservation{subscriptionAPIOnly(ctx), subscriptionlane.IncludedLaneExhausted},
		paidFallbackObservation{enterpriseSeat, subscriptionlane.EnterpriseSeatBound},
		paidFallbackObservation{enterpriseSeat && claudeEnterpriseCapRescue(ctx), subscriptionlane.EnterpriseCapRefused},
		paidFallbackObservation{pickedModelOnOwnPlan(ctx), subscriptionlane.OwnPlanPicked},
		paidFallbackObservation{subscriptionAttemptOnly(ctx), subscriptionlane.SubscriptionAttemptStarted},
	)
}

func paidFallbackBindingForModel(ctx context.Context, model string) subscriptionlane.Binding {
	includedLane := subscriptionStateModelsEnabled(ctx)
	_, exhaustedSetModel := modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx))[model]
	return stepPaidFallbackBinding(
		paidFallbackObservation{includedLane, subscriptionlane.IncludedLaneEntered},
		paidFallbackObservation{includedLane && (subscriptionAPIOnly(ctx) || exhaustedSetModel), subscriptionlane.IncludedLaneExhausted},
		paidFallbackObservation{claudeEnterpriseTarget(ctx, inference.Target{CatalogID: model}), subscriptionlane.EnterpriseSeatBound},
		paidFallbackObservation{pickedModelOnOwnPlan(ctx), subscriptionlane.OwnPlanPicked},
		paidFallbackObservation{subscriptionAttemptOnly(ctx), subscriptionlane.SubscriptionAttemptStarted},
	)
}

func servingEvent(creds *Credentials) subscriptionlane.Event {
	switch {
	case creds != nil && creds.OAuth:
		return subscriptionlane.SubscriptionServed
	case customerKeyFunded(creds):
		return subscriptionlane.CustomerKeyServed
	default:
		return subscriptionlane.OrganizationCreditsServed
	}
}

func paidFallbackEvent(customerKey *Credentials) subscriptionlane.Event {
	if customerKey != nil {
		return subscriptionlane.CustomerKeyServed
	}
	return subscriptionlane.OrganizationCreditsServed
}

func servingCommitted(ctx context.Context, binding subscriptionlane.Binding, on subscriptionlane.Event) bool {
	_, committed := subscriptionlane.Commit(billing.SubscriptionLaneFromContext(ctx), binding, on)
	return committed
}

func withSubscriptionAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, subscriptionOnlyAttemptKey{}, true)
}

func withIncludedLaneExhausted(ctx context.Context) context.Context {
	ctx = context.WithValue(billing.ReleaseLinkedFirst(ctx), subscriptionAPIOnlyKey{}, true)
	return credentials.WithSuppressedClaudeSubscription(withSuppressedCodexSubscription(ctx))
}
