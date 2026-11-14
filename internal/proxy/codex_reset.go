package proxy

import (
	"context"
	"errors"
	"slices"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/subscriptions"
)

type codexResetter interface {
	ResetCodex(context.Context, auth.SubscriptionOwner, string) (subscriptions.Lease, bool, error)
}

// leaseOrResetCodex leases a pool account and, when the personal Codex pool has
// none, attempts one earned-reset recovery per request before paid fallback.
func leaseOrResetCodex(ctx context.Context, leaser subscriptions.Leaser, observer *usage.Observer, owner auth.SubscriptionOwner, poolProvider subscriptions.Provider, model string, seen map[string]bool) (subscriptions.Lease, bool, error) {
	lease, present, err := leaser.Lease(ctx, owner, poolProvider, ClientIdentityFrom(ctx).SessionID)
	if poolProvider != subscriptions.ProviderCodex || !(errors.Is(err, subscriptions.ErrNoAvailableAccount) || err == nil && !present) {
		return lease, present, err
	}
	requestUsage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	resetter, ok := leaser.(codexResetter)
	if requestUsage == nil || requestUsage.ResetAttempted || !ok || !subscriptions.CodexAutoUsageResetEnabled(ctx, owner.SubscriberID) {
		return lease, present, err
	}
	requestUsage.ResetAttempted = true
	restored, recovered, resetErr := resetter.ResetCodex(ctx, codexResetOwner(owner, requestUsage), ClientIdentityFrom(ctx).SessionID)
	if resetErr != nil {
		// Recovery is best effort; the pool's own exhaustion verdict still decides the response.
		observability.FromContext(ctx).Warn("Codex earned reset recovery unavailable", "err", resetErr)
		return lease, present, err
	}
	if !recovered {
		return lease, present, err
	}
	delete(seen, restored.AccountID)
	delete(requestUsage.AttemptedAccounts, restored.AccountID+"\x00"+model)
	if observer != nil {
		observer.Forget(observer.Key([]byte("subscription-account:" + restored.AccountID)))
		observer.Forget(observer.Key([]byte(restored.AccessToken)))
	}
	return restored, true, nil
}

// codexResetOwner keeps quota-exhausted accounts eligible for recovery while
// still excluding accounts this request skipped for other reasons, such as a
// model denial.
func codexResetOwner(owner auth.SubscriptionOwner, requestUsage *ManagedSubscriptionUsage) auth.SubscriptionOwner {
	excluded := slices.DeleteFunc(slices.Clone(owner.ExcludedAccountIDs), func(accountID string) bool {
		_, quotaExhausted := requestUsage.QuotaExhaustedAccounts[accountID]
		return quotaExhausted
	})
	owner.ExcludedAccountIDs = excluded
	return owner
}
