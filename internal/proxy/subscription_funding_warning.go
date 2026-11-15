package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

// subscriptionSpendCapWarningMarker is prepended to a subscription-only turn
// a spend cap barred from paid capacity. The organization may still hold
// credits, so it names the cap and carries no top-up CTA.
const subscriptionSpendCapWarningMarker = routingMarkerPrefix +
	"a Weave router spend cap has been reached, so this turn is running on your own Anthropic subscription and paid model fallback is disabled.\n\n"

const subscriptionSpendCapWarningMarkerCodex = routingMarkerPrefix +
	"a Weave router spend cap has been reached, so this turn is running on your own ChatGPT (Codex) subscription and paid model fallback is disabled.\n\n"

// subscriptionSpendCapKeyWarningMarker replaces the spend-cap warning when
// the customer's own provider key, not their plan, serves the turn.
const subscriptionSpendCapKeyWarningMarker = routingMarkerPrefix +
	"a Weave router spend cap has been reached, so this turn is running on your own provider API key and paid model fallback is disabled.\n\n"

// subscriptionFundingWarning keeps funding policy separate from notice encoding.
type subscriptionFundingWarning struct {
	reason      billing.SubscriptionOnlyReason
	source      subscriptions.Provider
	customerKey bool
}

// newSubscriptionFundingWarning names the warning for a turn on model. A
// customer-key warning carries no source, so its echo matches on either ingress.
func newSubscriptionFundingWarning(ctx context.Context, reason billing.SubscriptionOnlyReason, source subscriptions.Provider, model string) subscriptionFundingWarning {
	if fundedByCustomerKey(ctx, model) {
		return subscriptionFundingWarning{reason: reason, customerKey: true}
	}
	return subscriptionFundingWarning{reason: reason, source: source}
}

// fundedByCustomerKey reports whether the customer's own provider key, not a
// linked plan the dispatcher would lease for model, funds the turn.
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
