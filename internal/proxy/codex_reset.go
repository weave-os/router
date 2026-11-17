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

func leaseOrResetCodex(ctx context.Context, leaser subscriptions.Leaser, observer *usage.Observer, owner auth.SubscriptionOwner, poolProvider subscriptions.Provider, model string, seen map[string]bool) (subscriptions.Lease, bool, error) {
	lease, present, err := leaser.Lease(ctx, owner, poolProvider, ClientIdentityFrom(ctx).SessionID)
	poolExhausted := errors.Is(err, subscriptions.ErrNoAvailableAccount) || err == nil && !present
	if poolProvider != subscriptions.ProviderCodex || !poolExhausted {
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

func codexResetOwner(owner auth.SubscriptionOwner, requestUsage *ManagedSubscriptionUsage) auth.SubscriptionOwner {
	owner.ExcludedAccountIDs = slices.DeleteFunc(slices.Clone(owner.ExcludedAccountIDs), func(accountID string) bool {
		_, quotaExhausted := requestUsage.QuotaExhaustedAccounts[accountID]
		return quotaExhausted
	})
	return owner
}
