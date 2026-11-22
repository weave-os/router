package proxy

import (
	"context"
	"errors"
	"slices"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
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

func linkedSeatAvailabilityOf(ctx context.Context, pool subscriptions.Provider, candidate credentials.ManagedSubscriptionCandidate, now time.Time) linkedSeatAvailability {
	resetPending := candidate.ResetAt.IsZero() || candidate.ResetAt.After(now)
	switch {
	case !candidate.Enabled || candidate.State == auth.SubscriptionAccountStateDisabled || candidate.State == auth.SubscriptionAccountStateReconnectRequired:
		return linkedSeatUnusable
	case candidate.State == auth.SubscriptionAccountStateExhausted && pool == subscriptions.ProviderClaude && requestcontext.SubscriptionExtraUsageFrom(ctx):
		return linkedSeatUsable
	case candidate.State == auth.SubscriptionAccountStateExhausted && resetPending:
		return linkedSeatUnusable
	case candidate.State == auth.SubscriptionAccountStateCooldown && resetPending && !candidate.CredentialRejected && !candidate.ResetAt.IsZero() && !candidate.ResetAt.After(now.Add(longestProviderThrottleCooldown)):
		return linkedSeatThrottled
	case candidate.State == auth.SubscriptionAccountStateCooldown && resetPending:
		return linkedSeatUnusable
	}
	return linkedSeatUsable
}

func linkedClaudeSeatConnected(ctx context.Context) bool {
	if !managedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude) {
		return false
	}
	candidates, snapshotPresent := ctx.Value(credentials.ManagedSubscriptionCandidatesContextKey{}).([]credentials.ManagedSubscriptionCandidate)
	return !snapshotPresent || slices.ContainsFunc(candidates, func(candidate credentials.ManagedSubscriptionCandidate) bool {
		return candidate.Provider == auth.SubscriptionProviderClaude && candidate.Enabled &&
			candidate.State != auth.SubscriptionAccountStateDisabled && candidate.State != auth.SubscriptionAccountStateReconnectRequired
	})
}

func linkedSeatsAvailability(ctx context.Context, pool subscriptions.Provider, now time.Time) linkedSeatAvailability {
	candidates, _ := ctx.Value(credentials.ManagedSubscriptionCandidatesContextKey{}).([]credentials.ManagedSubscriptionCandidate)
	availability := linkedSeatUsable
	for _, candidate := range candidates {
		if candidate.Provider != auth.SubscriptionProvider(pool) {
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
		return errors.Join(credentials.SubscriptionPoolExhausted(ctx), errLinkedSeatsThrottled)
	case linkedSeatUnusable:
		return errors.Join(credentials.SubscriptionPoolExhausted(ctx), errLinkedSeatsUnusable)
	case linkedSeatUsable:
	}
	return credentials.SubscriptionPoolExhausted(ctx)
}
