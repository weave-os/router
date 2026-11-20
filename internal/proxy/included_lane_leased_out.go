package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/subscriptions"
)

func leaseOrMarkLeasedOut(ctx context.Context, leaser subscriptions.Leaser, owner auth.SubscriptionOwner, pool subscriptions.Provider, model, sessionID string) (subscriptions.Lease, bool, error) {
	lease, present, err := leaser.Lease(ctx, owner, pool, sessionID)
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if state == nil || state.ClientAppRefused || (err == nil && present) || (err != nil && !errors.Is(err, subscriptions.ErrNoAvailableAccount)) {
		return lease, present, err
	}
	if state.LeasedOutPools == nil {
		state.LeasedOutPools = make(map[string]struct{})
	}
	state.LeasedOutPools[string(pool)+"\x00"+model] = struct{}{}
	return lease, present, err
}

func includedLaneCannotServe(ctx context.Context, pool subscriptions.Provider, target inference.Target, now time.Time) bool {
	return subscriptionProviderUnavailable(ctx, pool, now) || includedLaneLeasedOut(ctx, pool, target, now)
}

func includedLaneLeasedOut(ctx context.Context, pool subscriptions.Provider, target inference.Target, now time.Time) bool {
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if state == nil || !callerPlanThrottled(ctx, pool, now) {
		return false
	}
	for _, model := range []string{target.CatalogID, target.UpstreamID} {
		if _, leasedOut := state.LeasedOutPools[string(pool)+"\x00"+model]; model != "" && leasedOut && attemptedSeatsRateLimited(state, model, now) {
			return true
		}
	}
	return false
}

func attemptedSeatsRateLimited(state *ManagedSubscriptionUsage, model string, now time.Time) bool {
	for pair := range state.AttemptedAccounts {
		accountID, found := strings.CutSuffix(pair, "\x00"+model)
		if found && !state.ThrottledAccounts[accountID].After(now) && !state.QuotaExhaustedAccounts[accountID].After(now) {
			return false
		}
	}
	return true
}

func recordCallerCodexFailureOnLinkedSeat(ctx context.Context, creds *Credentials, attemptErr error, now time.Time) {
	if creds == nil || !creds.OAuth || creds.SubscriptionAccountID != "" || len(creds.AccountID) == 0 || upstreamStatus(attemptErr) != http.StatusTooManyRequests {
		return
	}
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	candidates, _ := ctx.Value(managedSubscriptionCandidatesKey{}).([]managedSubscriptionCandidate)
	if state == nil || state.IntendedModel == "" {
		return
	}
	for _, candidate := range candidates {
		if candidate.provider != auth.SubscriptionProviderCodex || candidate.externalAccountID == "" || candidate.externalAccountID != string(creds.AccountID) {
			continue
		}
		if resetAt, spent := codexQuotaExhaustion(attemptErr); spent {
			recordManagedQuotaProof(ctx, candidate.accountID, resetAt)
		} else {
			recordManagedThrottle(ctx, candidate.accountID, managedSubscriptionResetAt(attemptErr, now))
		}
		if state.AttemptedAccounts == nil {
			state.AttemptedAccounts = make(map[string]struct{})
		}
		state.AttemptedAccounts[candidate.accountID+"\x00"+state.IntendedModel] = struct{}{}
	}
}
