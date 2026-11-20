package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

type ManagedSubscriptionUsage struct {
	Served                     bool
	SubscriptionAttempted      bool
	CredentialSource           string
	OverageInUse               bool
	SubscriptionAccountID      string
	SubscriptionOwnerID        string
	SubscriptionTier           auth.SubscriptionTier
	IntendedModel              string
	AttemptedAccounts          map[string]struct{}
	LeasedOutPools             map[string]struct{}
	QuotaExhaustedAccounts     map[string]time.Time
	QuotaExhaustedProviders    map[subscriptions.Provider]time.Time
	ThrottledAccounts          map[string]time.Time
	ThrottledProviders         map[subscriptions.Provider]time.Time
	UnavailableProviders       map[subscriptions.Provider]time.Time
	ClientAppRefused           bool
	Finished                   bool
	WinningCredentials         *Credentials
	ClaudeSubscriptionPlan     providers.SubscriptionPlan
	ClaudePlanUnverified       bool
	ClaudeEnterpriseCapRefused bool
}

func WithManagedSubscriptionUsage(ctx context.Context) context.Context {
	if existing, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); existing != nil {
		return ctx
	}
	return context.WithValue(ctx, ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{})
}
