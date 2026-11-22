package credentials

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions/entitlement"
)

var ErrSubscriptionUnavailableForTurn = errors.New("subscription unavailable and paid fallback not permitted for this turn")

var subscriptionLaneOrgGates = map[refusal.Kind]error{
	refusal.CreditsExhausted:               ErrCreditsExhaustedSubscriptionUnavailable,
	refusal.SpendCapReached:                ErrSpendCapSubscriptionUnavailable,
	refusal.SubscriptionUnavailableForTurn: ErrSubscriptionUnavailableForTurn,
}

func LinkedFirst(ctx context.Context) bool {
	return billing.SubscriptionLaneFromContext(ctx) == subscriptionlane.LinkedFirst
}

func subscriptionLaneRefusal(ctx context.Context, on subscriptionlane.Event) refusal.Kind {
	next, _ := subscriptionlane.Step(billing.SubscriptionLaneFromContext(ctx), on)
	kind, _ := next.Refusal()
	return kind
}

func SubscriptionOnlyUnavailable(ctx context.Context) error {
	orgGate := subscriptionLaneOrgGates[subscriptionLaneRefusal(ctx, subscriptionlane.SubscriptionUnservable)]
	if window, spent := entitlement.SpentWindowFromContext(ctx); spent {
		return &IncludedAllowanceSpentError{Window: window, OrgGate: orgGate}
	}
	return orgGate
}

func SubscriptionLaneRefused(err error) bool {
	for _, gate := range subscriptionLaneOrgGates {
		if errors.Is(err, gate) {
			return true
		}
	}
	return false
}

var ErrCreditsExhaustedSubscriptionUnavailable = errors.New("credits exhausted and subscription unavailable for this turn")

var ErrSpendCapSubscriptionUnavailable = fmt.Errorf("spend cap reached: %w", ErrCreditsExhaustedSubscriptionUnavailable)

type IncludedAllowanceSpentError struct {
	Window  entitlement.SpentWindow
	OrgGate error
}

func (e *IncludedAllowanceSpentError) Error() string {
	return fmt.Sprintf("%s %s allowance spent: %v", e.Window.Plan, e.Window.Kind, e.OrgGate)
}

func (e *IncludedAllowanceSpentError) Unwrap() error { return e.OrgGate }

func releaseThrottledLinkedFirst(ctx context.Context) (context.Context, bool) {
	if !LinkedFirst(ctx) {
		return ctx, false
	}
	observability.FromContext(ctx).Info("Linked subscription rejected the turn; rerouting on organization credits")
	return billing.ReleaseLinkedFirst(ctx), true
}

type suppressClaudeSubscriptionContextKey struct{}

func WithSuppressedClaudeSubscription(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressClaudeSubscriptionContextKey{}, true)
}

func ClaudeSubscriptionSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(suppressClaudeSubscriptionContextKey{}).(bool)
	return v
}

func (b *Brokerage) RetryAfterSubscriptionOnlyBypass(ctx context.Context, log *slog.Logger, requestID, externalID string, decision router.Decision, managedSubscriptionCanServe func(ctx context.Context, provider, model string) bool) (context.Context, bool, error) {
	linkedSubscriptionRetry := false
	if billing.SubscriptionOnlyFromContext(ctx) {
		released, ok := releaseThrottledLinkedFirst(ctx)
		if ok {
			ctx = released
		} else if b.ManagedSubscriptions != nil && managedSubscriptionCanServe(ctx, decision.Provider, decision.Model) {
			ctx = WithSuppressedClaudeSubscription(ctx)
			linkedSubscriptionRetry = true
			log.Info("Subscription-only bypass hit retryable error; trying linked Claude accounts",
				"request_id", requestID, "external_id", externalID, "model", decision.Model)
		} else {
			log.Info("Subscription-only bypass hit retryable error; refusing instead of paid reroute",
				"request_id", requestID, "external_id", externalID)
			return ctx, false, SubscriptionOnlyUnavailable(ctx)
		}
	}
	return ctx, linkedSubscriptionRetry, nil
}
