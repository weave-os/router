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
	Skipped   int
}

func (r *Runtime) KeepWarm(ctx context.Context, policy KeepWarmPolicy) (KeepWarmResult, error) {
	store, ok := r.store.(KeepWarmStore)
	if !ok {
		return KeepWarmResult{}, errors.New("subscription keep-warm is not configured")
	}
	refreshedBefore := r.clock().Add(-policy.MaxAge)
	seats, err := store.ListSubscriptionAccountsDueForKeepWarm(ctx, refreshedBefore, policy.BatchLimit)
	if err != nil {
		return KeepWarmResult{}, err
	}
	result := KeepWarmResult{Due: len(seats)}
	for i, seat := range seats {
		if i > 0 && waitForRefresh(ctx, policy.SeatSpacing) != nil {
			break
		}
		refreshed, err := r.keepSeatWarm(ctx, seat, refreshedBefore)
		switch {
		case err != nil:
			result.Failed++
			observability.FromContext(ctx).Warn("Subscription keep-warm refresh failed", "account_id", seat.ID, "provider", seat.Provider, "err", err)
		case refreshed:
			result.Refreshed++
		default:
			result.Skipped++
		}
	}
	return result, ctx.Err()
}

func (r *Runtime) keepSeatWarm(ctx context.Context, seat *auth.SubscriptionAccount, refreshedBefore time.Time) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshLeaseTTL)
	defer cancel()
	owner := auth.SubscriptionOwner{SubscriberID: seat.SubscriberID}
	if owner.SubscriberID == "" {
		owner.APIKeyID = seat.EnrolledByAPIKeyID
	}
	current, err := r.store.LoadSubscriptionCredentials(refreshCtx, owner, seat.ID)
	if err != nil {
		return false, err
	}
	if current.TokenRefreshedAt != nil && !current.TokenRefreshedAt.Before(refreshedBefore) {
		return false, nil
	}
	_, err = r.refreshOwnedAccount(refreshCtx, owner, accountFromSubscription(seat))
	return err == nil, err
}
