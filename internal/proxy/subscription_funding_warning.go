package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
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

func routerNoticeForDecision(ctx context.Context, headers http.Header, decision router.Decision, marker string) translate.RouterNotice {
	source, _ := credentials.ManagedSubscriptionProviderFromUpstream(decision.Provider, decision.Model)
	return routerNoticeForRequest(ctx, headers, source, decision.Model, marker)
}

func fundedByCustomerKey(ctx context.Context, model string) bool {
	if !servedOnCustomerKey(ctx) {
		return false
	}
	m, found := catalog.ByID(model)
	return !found || !credentials.ManagedSubscriptionCanServe(ctx, m.PrimaryProvider(), model)
}

func (w subscriptionFundingWarning) text() string {
	switch w.reason {
	case billing.SubscriptionOnlySpendCapReached:
		if w.customerKey {
			return subscriptionSpendCapKeyWarningMarker
		}
		switch w.source {
		case subscriptions.ProviderCodex:
			return subscriptionSpendCapWarningMarkerCodex
		case subscriptions.ProviderClaude:
			return subscriptionSpendCapWarningMarker
		}
	}
	return ""
}

var subscriptionFundingWarnings = []subscriptionFundingWarning{
	{reason: billing.SubscriptionOnlySpendCapReached, source: subscriptions.ProviderClaude},
	{reason: billing.SubscriptionOnlySpendCapReached, source: subscriptions.ProviderCodex},
	{reason: billing.SubscriptionOnlySpendCapReached, customerKey: true},
}
