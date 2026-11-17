package proxy

import (
	"context"
	"errors"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

var (
	errLinkedSeatsUnusable  = errors.New("every linked subscription account is disabled, disconnected or spent")
	errLinkedSeatsThrottled = errors.New("every usable linked subscription account is in a provider rate-limit cooldown")
)

const longestProviderThrottleCooldown = 24 * time.Hour

type linkedSeatAvailability int

const (
	linkedSeatUsable linkedSeatAvailability = iota
	linkedSeatThrottled
	linkedSeatUnusable
)

func linkedSeatAvailabilityOf(ctx context.Context, pool subscriptions.Provider, candidate managedSubscriptionCandidate, now time.Time) linkedSeatAvailability {
	resetPending := candidate.resetAt.IsZero() || candidate.resetAt.After(now)
	switch {
	case !candidate.enabled || candidate.state == auth.SubscriptionAccountStateDisabled || candidate.state == auth.SubscriptionAccountStateReconnectRequired:
		return linkedSeatUnusable
	case candidate.state == auth.SubscriptionAccountStateExhausted && pool == subscriptions.ProviderClaude && requestcontext.SubscriptionExtraUsageFrom(ctx):
		return linkedSeatUsable
	case candidate.state == auth.SubscriptionAccountStateExhausted && resetPending:
		return linkedSeatUnusable
	case candidate.state == auth.SubscriptionAccountStateCooldown && resetPending && !candidate.credentialRejected && !candidate.resetAt.IsZero() && !candidate.resetAt.After(now.Add(longestProviderThrottleCooldown)):
		return linkedSeatThrottled
	case candidate.state == auth.SubscriptionAccountStateCooldown && resetPending:
		return linkedSeatUnusable
	}
	return linkedSeatUsable
}

func linkedSeatsAvailability(ctx context.Context, pool subscriptions.Provider, now time.Time) linkedSeatAvailability {
	candidates, _ := ctx.Value(managedSubscriptionCandidatesKey{}).([]managedSubscriptionCandidate)
	availability := linkedSeatUsable
	for _, candidate := range candidates {
		if candidate.provider != auth.SubscriptionProvider(pool) {
			continue
		}
		switch linkedSeatAvailabilityOf(ctx, pool, candidate, now) {
		case linkedSeatUsable:
			return linkedSeatUsable
		case linkedSeatThrottled:
			availability = linkedSeatThrottled
		case linkedSeatUnusable:
			if availability == linkedSeatUsable {
				availability = linkedSeatUnusable
			}
		}
	}
	return availability
}

func linkedPoolExhausted(ctx context.Context, pool subscriptions.Provider, now time.Time) error {
	switch linkedSeatsAvailability(ctx, pool, now) {
	case linkedSeatThrottled:
		return errors.Join(subscriptionPoolExhausted(ctx), errLinkedSeatsThrottled)
	case linkedSeatUnusable:
		return errors.Join(subscriptionPoolExhausted(ctx), errLinkedSeatsUnusable)
	case linkedSeatUsable:
	}
	return subscriptionPoolExhausted(ctx)
}
