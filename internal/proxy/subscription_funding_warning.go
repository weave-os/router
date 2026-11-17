package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

const subscriptionSpendCapWarningMarker = routingMarkerPrefix +
	"a Weave router spend cap has been reached, so this turn is running on your own Anthropic subscription and paid model fallback is disabled.\n\n"

const subscriptionSpendCapWarningMarkerCodex = routingMarkerPrefix +
	"a Weave router spend cap has been reached, so this turn is running on your own ChatGPT (Codex) subscription and paid model fallback is disabled.\n\n"

const subscriptionSpendCapKeyWarningMarker = routingMarkerPrefix +
	"a Weave router spend cap has been reached, so this turn is running on your own provider API key and paid model fallback is disabled.\n\n"

type subscriptionFundingWarning struct {
	reason      billing.SubscriptionOnlyReason
	source      subscriptions.Provider
	customerKey bool
}

func fundedByCustomerKey(ctx context.Context, model string) bool {
	if !servedOnCustomerKey(ctx) {
		return false
	}
	m, found := catalog.ByID(model)
	return !found || !managedSubscriptionCanServe(ctx, m.PrimaryProvider(), model)
}

func (w subscriptionFundingWarning) text() string {
	switch w.reason {
	case billing.SubscriptionOnlySpendCapReached:
		if w.customerKey {
			return subscriptionSpendCapKeyWarningMarker
		}
		if w.source == subscriptions.ProviderCodex {
			return subscriptionSpendCapWarningMarkerCodex
		}
		return subscriptionSpendCapWarningMarker
	default:
		return ""
	}
}

var subscriptionFundingWarnings = []subscriptionFundingWarning{
	{reason: billing.SubscriptionOnlySpendCapReached, source: subscriptions.ProviderClaude},
	{reason: billing.SubscriptionOnlySpendCapReached, source: subscriptions.ProviderCodex},
	{reason: billing.SubscriptionOnlySpendCapReached, customerKey: true},
}
