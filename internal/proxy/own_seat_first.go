package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

func callerLoginServesFirst(ctx context.Context, provider, model string, observer *usage.Observer, models *subscriptionModelAccess, now time.Time) bool {
	current := CredentialsFromContext(ctx)
	if current == nil || !current.OAuth {
		return false
	}
	if pickedModelOnOwnPlan(ctx) {
		return true
	}
	token := ownSeatFirstToken(ctx, observer)
	return token != "" && (provider != providers.ProviderAnthropic || !models.denied([]byte(token), model, now))
}

func ownSeatFirstToken(ctx context.Context, observer *usage.Observer) string {
	if !flags.BoolOr(ctx, flags.KeyOwnSeatFirst, false) {
		return ""
	}
	current := CredentialsFromContext(ctx)
	if current == nil || !current.OAuth {
		return ""
	}
	inbound := string(current.APIKey)
	if observer == nil {
		return inbound
	}
	key := observer.Key([]byte(inbound))
	if observer.Rejected(key) {
		return ""
	}
	snap, observed := observer.Snapshot(key)
	switch {
	case !observed:
		return inbound
	case flags.BoolOr(ctx, flags.KeyOwnOverageBeforePool, false) && snap.Exhausted():
		return ""
	case !flags.BoolOr(ctx, flags.KeyOwnOverageBeforePool, false) && snap.BillableOrExhausted():
		return ""
	}
	return inbound
}
