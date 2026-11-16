package subscriptions

import (
	"context"
	"slices"
	"time"
	"weave-os/router/internal/auth"
)

func LeaseRequired(ctx context.Context, leaser Leaser, owner auth.SubscriptionOwner, provider Provider, session string) (Lease, bool, error) {
	if owner.RequiredAccountID == "" {
		return Lease{}, false, ErrNoAvailableAccount
	}
	lease, present, err := leaser.Lease(ctx, owner, provider, session)
	if err != nil || !present || lease.AccountID != owner.RequiredAccountID || lease.AccessToken == "" || lease.ProviderAccount == "" || lease.State != auth.SubscriptionAccountStateActive {
		lease.Release()
		return Lease{}, present, ErrNoAvailableAccount
	}
	return lease, true, nil
}

func eligibleSubscriptionAccounts(ctx context.Context, requester auth.SubscriptionOwner, provider Provider, accounts []*auth.SubscriptionAccount, now time.Time) ([]*auth.SubscriptionAccount, bool) {
	present := false
	eligible := make([]*auth.SubscriptionAccount, 0, len(accounts))
	for _, candidate := range accounts {
		if candidate.SubscriberID == "" || Provider(candidate.Provider) != provider || (requester.RequiredAccountID != "" && requester.RequiredAccountID != candidate.ID || slices.Contains(requester.ExcludedAccountIDs, candidate.ID)) {
			continue
		}
		present = true
		var cooldown time.Time
		if candidate.CooldownUntil != nil {
			cooldown = *candidate.CooldownUntil
		}
		if candidate.Enabled && subscriptionAccountEligible(ctx, provider, candidate.State, cooldown, now) {
			eligible = append(eligible, candidate)
		}
	}
	return eligible, present
}
