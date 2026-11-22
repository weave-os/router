package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions"
)

func (s *Service) markManagedSubscriptionServed(ctx context.Context, credentialCtx context.Context, leases ...subscriptions.Lease) {
	var lease subscriptions.Lease
	if len(leases) > 0 {
		lease = leases[0]
	}
	usage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	if usage != nil {
		served, stepped := subscriptionlane.Step(billing.SubscriptionLaneFromContext(ctx), subscriptionlane.SubscriptionServed)
		usage.Served = stepped && served == subscriptionlane.ServedOnSubscription
		usage.SubscriptionAccountID = lease.AccountID
		usage.SubscriptionOwnerID = lease.OwnerID
		usage.SubscriptionTier = lease.Tier
		if creds := CredentialsFromContext(credentialCtx); creds != nil {
			usage.CredentialSource = creds.Source
			if s.credentials.UsageObserver != nil && creds.Source == credSourceSubscription {
				if snapshot, observed := s.credentials.UsageObserver.Snapshot(s.credentials.UsageObserver.Key(creds.APIKey)); observed {
					usage.OverageInUse = snapshot.OverageInUse
				}
			}
		}
	}
}

func managedSubscriptionCredentialSource(ctx context.Context) string {
	usage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	if usage == nil || !usage.Served {
		return ""
	}
	return usage.CredentialSource
}

func managedSubscriptionServed(ctx context.Context) bool {
	usage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	return usage != nil && usage.Served
}

func (s *Service) subscriptionOverageInUse(ctx context.Context) bool {
	if !servedOnSubscription(ctx) {
		return false
	}
	if creds := CredentialsFromContext(ctx); creds != nil && creds.Source == credSourceCodexSubscription {
		return false
	}
	if managedSubscriptionCredentialSource(ctx) == credSourceCodexSubscription {
		return false
	}
	headers := UnifiedLimitHeadersFrom(ctx)
	if claim, present := headers["anthropic-ratelimit-unified-representative-claim"]; present {
		return usage.PaidAnthropicOverage(
			usage.AnthropicClaim(claim), headers["anthropic-ratelimit-unified-overage-in-use"])
	}
	if managedUsage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); managedUsage != nil && managedUsage.Served {
		return managedUsage.OverageInUse
	}
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.Source != credSourceSubscription || s.credentials.UsageObserver == nil {
		return false
	}
	snapshot, observed := s.credentials.UsageObserver.Snapshot(s.credentials.UsageObserver.Key(creds.APIKey))
	return observed && snapshot.OverageInUse
}

func (s *Service) costNeutralSubscriptionServed(ctx context.Context) bool {
	return servedOnSubscription(ctx) && !s.subscriptionOverageInUse(ctx)
}
