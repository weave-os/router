package subscriptions

import (
	"context"
	"errors"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

type KeepWarmStore interface {
	ListSubscriptionAccountsDueForKeepWarm(context.Context, time.Time, int32) ([]*auth.SubscriptionAccount, error)
}

type KeepWarmPolicy struct {
	MaxAge      time.Duration
	BatchLimit  int32
	SeatSpacing time.Duration
}

type KeepWarmResult struct {
	Due       int
	Refreshed int
	Failed    int
}

func (r *Runtime) KeepWarm(ctx context.Context, policy KeepWarmPolicy) (KeepWarmResult, error) {
	store, ok := r.store.(KeepWarmStore)
	if !ok {
		return KeepWarmResult{}, errors.New("subscription keep-warm is not configured")
	}
	seats, err := store.ListSubscriptionAccountsDueForKeepWarm(ctx, r.clock().Add(-policy.MaxAge), policy.BatchLimit)
	if err != nil {
		return KeepWarmResult{}, err
	}
	result := KeepWarmResult{Due: len(seats)}
	for i, seat := range seats {
		if i > 0 && waitForRefresh(ctx, policy.SeatSpacing) != nil {
			break
		}
		if err := r.keepSeatWarm(ctx, seat); err != nil {
			result.Failed++
			observability.FromContext(ctx).Warn("Subscription keep-warm refresh failed", "account_id", seat.ID, "provider", seat.Provider, "err", err)
			continue
		}
		result.Refreshed++
	}
	return result, ctx.Err()
}

func (r *Runtime) keepSeatWarm(ctx context.Context, seat *auth.SubscriptionAccount) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshLeaseTTL)
	defer cancel()
	owner := auth.SubscriptionOwner{SubscriberID: seat.SubscriberID}
	if owner.SubscriberID == "" {
		owner.APIKeyID = seat.EnrolledByAPIKeyID
	}
	_, err := r.refreshOwnedAccount(refreshCtx, owner, accountFromSubscription(seat))
	return err
}
