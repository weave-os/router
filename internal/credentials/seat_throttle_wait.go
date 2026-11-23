package credentials

import (
	"context"
	"errors"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions"
)

const shortSeatThrottleWait = 30 * time.Second

func shortestSeatThrottle(ctx context.Context, pool subscriptions.Provider, now time.Time) (time.Duration, bool) {
	candidates, _ := ctx.Value(ManagedSubscriptionCandidatesContextKey{}).([]ManagedSubscriptionCandidate)
	usageState, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	var shortest time.Duration
	for _, candidate := range candidates {
		if candidate.Provider != auth.SubscriptionProvider(pool) || !candidate.Enabled || !ManagedAccountThrottled(ctx, candidate, now) {
			continue
		}
		reset := candidate.ResetAt
		if usageState != nil && usageState.ThrottledAccounts[candidate.AccountID].After(reset) {
			reset = usageState.ThrottledAccounts[candidate.AccountID]
		}
		if wait := reset.Sub(now); wait > 0 && (shortest == 0 || wait < shortest) {
			shortest = wait
		}
	}
	return shortest, shortest > 0 && shortest <= shortSeatThrottleWait
}

func WaitOutShortSeatThrottle(ctx context.Context, provider, model string, err error, now time.Time, sleep func(context.Context, time.Duration) error, waited *time.Duration) bool {
	pool, eligible := ManagedSubscriptionProviderFromUpstream(provider, model)
	if !eligible || !IsSubscriptionPoolError(err) {
		return false
	}
	wait, short := shortestSeatThrottle(ctx, pool, now)
	if !short || *waited+wait > shortSeatThrottleWait {
		return false
	}
	*waited += wait
	observability.FromContext(ctx).Info("Every subscription seat is briefly rate-limited; waiting for the first cooldown", "provider", pool, "wait", wait)
	if sleepFor(ctx, sleep, wait) != nil {
		return false
	}
	releaseCooledSeats(ctx, now.Add(wait))
	return true
}

func releaseCooledSeats(ctx context.Context, at time.Time) {
	usageState, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if usageState == nil {
		return
	}
	for pair := range usageState.AttemptedAccounts {
		account, _, _ := strings.Cut(pair, "\x00")
		if reset, throttled := usageState.ThrottledAccounts[account]; throttled && !reset.After(at) {
			delete(usageState.AttemptedAccounts, pair)
		}
	}
}

func sleepFor(ctx context.Context, sleep func(context.Context, time.Duration) error, d time.Duration) error {
	if sleep != nil {
		return sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func SeatsThrottled(ctx context.Context, pool subscriptions.Provider, now time.Time) bool {
	candidates, _ := ctx.Value(ManagedSubscriptionCandidatesContextKey{}).([]ManagedSubscriptionCandidate)
	for _, candidate := range candidates {
		if candidate.Provider == auth.SubscriptionProvider(pool) && candidate.Enabled && ManagedAccountThrottled(ctx, candidate, now) {
			return true
		}
	}
	return false
}

func ThrottledPoolRefusal(ctx context.Context, pool subscriptions.Provider, callerHasToken bool, now time.Time) error {
	_, withinShortWait := shortestSeatThrottle(ctx, pool, now)
	if !billing.WeaveCreditsDeniedFromContext(ctx) || callerHasToken && !withinShortWait {
		return nil
	}
	return SubscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolThrottled)
}

func IsSubscriptionPoolError(err error) bool {
	return errors.Is(err, ErrSubscriptionPoolExhausted) || errors.Is(err, ErrSubscriptionPoolUnavailable) || errors.Is(err, ErrClaudeEnterpriseSubscriptionUnavailable)
}
