package proxy

import (
	"context"
	"errors"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

var errLinkedSeatsUnusable = errors.New("every linked subscription account is disabled, disconnected or spent")

func linkedSeatsUnusable(ctx context.Context, pool subscriptions.Provider, now time.Time) bool {
	candidates, _ := ctx.Value(managedSubscriptionCandidatesKey{}).([]managedSubscriptionCandidate)
	linked := 0
	for _, candidate := range candidates {
		if candidate.provider != auth.SubscriptionProvider(pool) {
			continue
		}
		linked++
		spent := (candidate.state == auth.SubscriptionAccountStateCooldown || candidate.state == auth.SubscriptionAccountStateExhausted) && candidate.resetAt.After(now)
		if candidate.enabled && candidate.state != auth.SubscriptionAccountStateDisabled && candidate.state != auth.SubscriptionAccountStateReconnectRequired && !spent {
			return false
		}
	}
	return linked > 0
}

func linkedPoolExhausted(ctx context.Context, pool subscriptions.Provider, now time.Time) error {
	if linkedSeatsUnusable(ctx, pool, now) {
		return errors.Join(subscriptionPoolExhausted(ctx), errLinkedSeatsUnusable)
	}
	return subscriptionPoolExhausted(ctx)
}
