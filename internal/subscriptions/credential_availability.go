package subscriptions

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
)

func subscriptionCredentialAvailable(ctx context.Context, provider Provider, credentials auth.SubscriptionCredentials, now time.Time) bool {
	var cooldownUntil time.Time
	if credentials.CooldownUntil != nil {
		cooldownUntil = *credentials.CooldownUntil
	}
	if requestcontext.SubscriptionQuotaRecoveryFrom(ctx) {
		// Quota management may refresh exhausted accounts without admitting inference.
		return credentials.Enabled && resetAccountStateEligible(credentials.State)
	}
	return credentials.Enabled && subscriptionAccountEligible(ctx, provider, credentials.State, cooldownUntil, now)
}
