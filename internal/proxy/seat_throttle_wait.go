package proxy

import (
	"context"
	"strings"
	"time"

	"weave-os/router/internal/subscriptionlane"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

const shortSeatThrottleWait = 30 * time.Second

func shortestSeatThrottle(ctx context.Context, pool subscriptions.Provider, now time.Time) (time.Duration, bool) {
	candidates, _ := ctx.Value(managedSubscriptionCandidatesKey{}).([]managedSubscriptionCandidate)
	usageState, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	var shortest time.Duration
	for _, candidate := range candidates {
		if candidate.provider != auth.SubscriptionProvider(pool) || !candidate.enabled || !managedAccountThrottled(ctx, candidate, now) {
			continue
		}
		reset := candidate.resetAt
		if usageState != nil && usageState.ThrottledAccounts[candidate.accountID].After(reset) {
			reset = usageState.ThrottledAccounts[candidate.accountID]
		}
		if wait := reset.Sub(now); wait > 0 && (shortest == 0 || wait < shortest) {
			shortest = wait
		}
	}
	return shortest, shortest > 0 && shortest <= shortSeatThrottleWait
}

func waitOutShortSeatThrottle(ctx context.Context, provider, model string, err error, now time.Time, sleep func(context.Context, time.Duration) error, waited *time.Duration) bool {
	pool, eligible := managedSubscriptionProviderFromUpstream(provider, model)
	if !eligible || !isSubscriptionPoolError(err) {
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

func seatsThrottled(ctx context.Context, pool subscriptions.Provider, now time.Time) bool {
	candidates, _ := ctx.Value(managedSubscriptionCandidatesKey{}).([]managedSubscriptionCandidate)
	for _, candidate := range candidates {
		if candidate.provider == auth.SubscriptionProvider(pool) && candidate.enabled && managedAccountThrottled(ctx, candidate, now) {
			return true
		}
	}
	return false
}

func throttledPoolRefusal(ctx context.Context, pool subscriptions.Provider, callerHasToken bool, now time.Time) error {
	_, withinShortWait := shortestSeatThrottle(ctx, pool, now)
	if !billing.WeaveCreditsDeniedFromContext(ctx) || callerHasToken && !withinShortWait {
		return nil
	}
	return subscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolThrottled)
}

func anotherSeatPoolCanServe(ctx context.Context, admission seatAdmission, models []string, target router.Decision) bool {
	pool, _ := managedSubscriptionProviderFromUpstream(target.Provider, target.Model)
	for _, model := range models {
		entry, _ := catalog.ByID(model)
		for _, binding := range entry.Providers {
			other, eligible := managedSubscriptionProviderFromUpstream(binding.Provider, model)
			if eligible && other != pool && admission.mayAdmit(ctx, binding.Provider, model) {
				return true
			}
		}
	}
	return false
}
