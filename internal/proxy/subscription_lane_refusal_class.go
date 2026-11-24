package proxy

import (
	"errors"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/refusal"
)

func subscriptionLaneRefusalClass(err error) ingress.DispatchErrorClass {
	var allowanceSpent *credentials.IncludedAllowanceSpentError
	switch {
	case errors.As(err, &allowanceSpent):
		kind, reason := ingress.DispatchErrorCreditsExhausted, refusal.CreditsExhausted
		switch {
		case errors.Is(err, credentials.ErrSpendCapSubscriptionUnavailable):
			kind, reason = ingress.DispatchErrorSpendCapReached, refusal.SpendCapReached
		case errors.Is(err, credentials.ErrSubscriptionUnavailableForTurn):
			kind, reason = ingress.DispatchErrorSubscriptionUnavailableForTurn, refusal.SubscriptionUnavailableForTurn
		}
		return refusalClass(kind, reason, includedAllowanceSpentMessage(allowanceSpent),
			"warn", "Subscription-only request refused: included allowance spent and subscription unavailable")
	case errors.Is(err, credentials.ErrSpendCapSubscriptionUnavailable):
		return refusalClass(ingress.DispatchErrorSpendCapReached, refusal.SpendCapReached,
			"A Weave router spend cap has been reached and your subscription can't serve this turn (rate-limited, or the requested model isn't subscription-covered).",
			"warn", "Subscription-only request refused: spend cap reached and subscription unavailable")
	case errors.Is(err, credentials.ErrCreditsExhaustedSubscriptionUnavailable):
		return refusalClass(ingress.DispatchErrorCreditsExhausted, refusal.CreditsExhausted,
			"Your Weave router credits are exhausted and your subscription can't serve this turn (rate-limited, or the requested model isn't subscription-covered). Add credits to re-enable paid routing: "+topUpURL,
			"warn", "Subscription-only request refused: credits exhausted and subscription unavailable")
	case errors.Is(err, credentials.ErrSubscriptionUnavailableForTurn):
		return refusalClass(ingress.DispatchErrorSubscriptionUnavailableForTurn, refusal.SubscriptionUnavailableForTurn,
			"Your subscription can't serve this turn (rate-limited, or the requested model isn't subscription-covered), and this turn can't fall back to paid routing.",
			"warn", "Request refused: subscription unavailable and paid fallback not permitted for this turn")
	}
	return ingress.DispatchErrorClass{}
}
