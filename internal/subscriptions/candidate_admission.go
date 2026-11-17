package subscriptions

import (
	"context"
	"slices"
	"time"
	"weave-os/router/internal/auth"
)

func eligibleSubscriptionAccounts(ctx context.Context, requester auth.SubscriptionOwner, provider Provider, accounts []*auth.SubscriptionAccount, now time.Time) ([]*auth.SubscriptionAccount, bool) {
	present := false
	eligible := make([]*auth.SubscriptionAccount, 0, len(accounts))
	for _, candidate := range accounts {
		if candidate.SubscriberID == "" || Provider(candidate.Provider) != provider || slices.Contains(requester.ExcludedAccountIDs, candidate.ID) {
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
