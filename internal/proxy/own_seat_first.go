package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/requestcontext"
)

func callerLoginServesFirst(ctx context.Context, model string, observer *usage.Observer, models *credentials.SubscriptionModelAccess, now time.Time) bool {
	current := credentials.CredentialsFromContext(ctx)
	if current == nil || !current.OAuth {
		return false
	}
	if pickedModelOnOwnPlan(ctx) {
		return true
	}
	token := ownSeatFirstToken(ctx, current, observer)
	return token != "" && !models.Denied([]byte(token), model, now)
}

func ownSeatFirstToken(ctx context.Context, current *requestcontext.Credentials, observer *usage.Observer) string {
	if !flags.BoolOr(ctx, flags.KeyOwnSeatFirst, false) {
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
	snapshot, observed := observer.Snapshot(key)
	if observed && snapshot.BillableOrExhausted() && !ownOverageServesFirst(ctx, snapshot) {
		return ""
	}
	return inbound
}

func ownOverageServesFirst(ctx context.Context, snapshot usage.Snapshot) bool {
	return snapshot.OverageInUse && ownOverageBeforePool(ctx)
}

func ownOverageBeforePool(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyOwnSeatFirst, false) && flags.BoolOr(ctx, flags.KeyOwnOverageBeforePool, false)
}
