package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/subscriptions"
)

func leaseOrMarkLeasedOut(ctx context.Context, leaser subscriptions.Leaser, owner auth.SubscriptionOwner, pool subscriptions.Provider, model, sessionID string) (subscriptions.Lease, bool, error) {
	lease, present, err := leaser.Lease(ctx, owner, pool, sessionID)
	state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
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
	return credentials.SubscriptionProviderUnavailable(ctx, pool, now) || includedLaneLeasedOut(ctx, pool, target, now)
}

func includedLaneLeasedOut(ctx context.Context, pool subscriptions.Provider, target inference.Target, now time.Time) bool {
	state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
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

func attemptedSeatsRateLimited(state *credentials.ManagedSubscriptionUsage, model string, now time.Time) bool {
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
	state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	candidates, _ := ctx.Value(credentials.ManagedSubscriptionCandidatesContextKey{}).([]credentials.ManagedSubscriptionCandidate)
	callerUserID := subscriptions.CodexUserIDFromJWT(string(creds.APIKey))
	if state == nil || state.IntendedModel == "" || callerUserID == "" {
		return
	}
	resetAt, spent := codexQuotaExhaustion(attemptErr)
	if resetAt.IsZero() {
		resetAt = managedSubscriptionResetAt(attemptErr, now)
	}
	for _, candidate := range candidates {
		if candidate.Provider != auth.SubscriptionProviderCodex || candidate.ExternalAccountID != string(creds.AccountID) || candidate.ProviderUserID != callerUserID {
			continue
		}
		if spent {
			recordManagedQuotaProof(ctx, candidate.AccountID, resetAt)
		} else {
			recordManagedThrottle(ctx, candidate.AccountID, resetAt)
		}
		if state.AttemptedAccounts == nil {
			state.AttemptedAccounts = make(map[string]struct{})
		}
		state.AttemptedAccounts[candidate.AccountID+"\x00"+state.IntendedModel] = struct{}{}
	}
}
