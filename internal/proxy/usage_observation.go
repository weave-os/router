package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
)

func attemptWithUsageObservation(ctx context.Context, observer *usage.Observer, attempt dispatchAttempt, decision router.Decision, upstream providers.Client) error {
	credentials := CredentialsFromContext(ctx)
	if observer == nil || credentials == nil || !credentials.OAuth {
		return attempt(ctx, decision, upstream)
	}
	key := observer.Key(credentials.APIKey)
	var aliases []usage.CredentialKey
	var generation *int64
	if credentials.SubscriptionAccountID != "" {
		aliases = append(aliases, key)
		key = observer.Key([]byte("subscription-account:" + credentials.SubscriptionAccountID))
		generation = &credentials.SubscriptionQuotaGeneration
	}
	observation := observer.BeginObservation(key, generation, aliases...)
	defer observation.Close()
	err := attempt(usage.WithObservation(ctx, observation), decision, upstream)
	if credentials.Source == credSourceCodexSubscription {
		if resetAt, exhausted := codexQuotaExhaustion(err); exhausted {
			observation.Record(usage.Snapshot{Primary: codexExhaustionWindow(resetAt, time.Now())})
		}
	}
	return err
}
