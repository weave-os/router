package credentials

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

const ManagedUsageRefusalCooldown = 15 * time.Minute

func CooldownRefusedManagedSeat(ctx context.Context, leaser subscriptions.Leaser, now time.Time, owner auth.SubscriptionOwner, poolProvider subscriptions.Provider, lease subscriptions.Lease, attemptErr error) bool {
	cooldownUntil := now.Add(ManagedUsageRefusalCooldown)
	if resetAt := ManagedSubscriptionResetAt(attemptErr, now); resetAt.After(cooldownUntil) {
		cooldownUntil = resetAt
	}
	if err := leaser.Cooldown(ctx, owner, poolProvider, lease.AccountID, cooldownUntil); err != nil {
		observability.FromContext(ctx).Error("Failed to persist refused subscription account cooldown",
			"provider", poolProvider, "account_id", lease.AccountID, "err", err)
	}
	observability.FromContext(ctx).Warn("Subscription account refused on usage limits; rotating",
		"provider", poolProvider, "account_id", lease.AccountID, "cooldown_until", cooldownUntil)
	return true
}

func RecordManagedQuotaProof(ctx context.Context, accountID string, resetAt time.Time) {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		if state.QuotaExhaustedAccounts == nil {
			state.QuotaExhaustedAccounts = make(map[string]time.Time)
		}
		state.QuotaExhaustedAccounts[accountID] = resetAt
	}
}

func RecordManagedThrottle(ctx context.Context, accountID string, cooldownUntil time.Time) {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		if state.ThrottledAccounts == nil {
			state.ThrottledAccounts = make(map[string]time.Time)
		}
		state.ThrottledAccounts[accountID] = cooldownUntil
	}
}

func ManagedSubscriptionResetAt(err error, now time.Time) time.Time {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) {
		return now.Add(time.Minute)
	}
	if retryAfter := upstream.Headers.Get("Retry-After"); retryAfter != "" {
		if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil && seconds > 0 {
			return now.Add(time.Duration(seconds) * time.Second)
		}
		if resetAt, parseErr := http.ParseTime(retryAfter); parseErr == nil && resetAt.After(now) {
			return resetAt
		}
	}
	return now.Add(time.Minute)
}
