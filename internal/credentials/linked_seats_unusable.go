package credentials

import (
	"context"
	"errors"
	"slices"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

var (
	ErrLinkedSeatsUnusable  = errors.New("every linked subscription account is disabled, disconnected or spent")
	ErrLinkedSeatsThrottled = errors.New("every usable linked subscription account is in a provider rate-limit cooldown")
)

const longestProviderThrottleCooldown = 24 * time.Hour

type linkedSeatAvailability int

const (
	linkedSeatUsable linkedSeatAvailability = iota
	linkedSeatThrottled
	linkedSeatUnusable
)

func linkedSeatAvailabilityOf(ctx context.Context, pool subscriptions.Provider, candidate ManagedSubscriptionCandidate, now time.Time) linkedSeatAvailability {
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

func LinkedClaudeSeatConnected(ctx context.Context) bool {
	if !ManagedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude) {
		return false
	}
	candidates, snapshotPresent := ctx.Value(ManagedSubscriptionCandidatesContextKey{}).([]ManagedSubscriptionCandidate)
	return !snapshotPresent || slices.ContainsFunc(candidates, func(candidate ManagedSubscriptionCandidate) bool {
		return candidate.Provider == auth.SubscriptionProviderClaude && candidate.Enabled &&
			candidate.State != auth.SubscriptionAccountStateDisabled && candidate.State != auth.SubscriptionAccountStateReconnectRequired
	})
}

func linkedSeatsAvailability(ctx context.Context, pool subscriptions.Provider, now time.Time) linkedSeatAvailability {
	candidates, _ := ctx.Value(ManagedSubscriptionCandidatesContextKey{}).([]ManagedSubscriptionCandidate)
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

func LinkedPoolExhausted(ctx context.Context, pool subscriptions.Provider, now time.Time) error {
	switch linkedSeatsAvailability(ctx, pool, now) {
	case linkedSeatThrottled:
		return errors.Join(SubscriptionPoolExhausted(ctx), ErrLinkedSeatsThrottled)
	case linkedSeatUnusable:
		return errors.Join(SubscriptionPoolExhausted(ctx), ErrLinkedSeatsUnusable)
	case linkedSeatUsable:
	}
	return SubscriptionPoolExhausted(ctx)
}
