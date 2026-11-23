package credentials

import (
	"context"
	"time"

	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/requestcontext"
)

func CallerLoginServesFirst(ctx context.Context, model string, observer *usage.Observer, models *SubscriptionModelAccess, now time.Time) bool {
	current := CredentialsFromContext(ctx)
	if current == nil || !current.OAuth {
		return false
	}
	if PickedModelOnOwnPlan(ctx) {
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
	if observed && snapshot.BillableOrExhausted() && !OwnOverageServesFirst(ctx, snapshot) {
		return ""
	}
	return inbound
}

func OwnOverageServesFirst(ctx context.Context, snapshot usage.Snapshot) bool {
	return snapshot.OverageInUse && OwnOverageBeforePool(ctx)
}

func OwnOverageBeforePool(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyOwnSeatFirst, false) && flags.BoolOr(ctx, flags.KeyOwnOverageBeforePool, false)
}

type PickedModelOnOwnPlanContextKey struct{}

func PickedModelOnOwnPlan(ctx context.Context) bool {
	onOwnPlan, _ := ctx.Value(PickedModelOnOwnPlanContextKey{}).(bool)
	return onOwnPlan
}
