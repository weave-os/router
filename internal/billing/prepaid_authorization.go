package billing

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"weave-os/router/internal/subscriptions/entitlement"
)

var (
	ErrPrepaidAuthorizationConflict = errors.New("billing: conflicting prepaid authorization")
	ErrPrepaidAuthorizationNotFound = errors.New("billing: prepaid authorization not found")
)

type PrepaidAuthorizationState string

const (
	PrepaidAuthorizationReserved PrepaidAuthorizationState = "reserved"
	PrepaidAuthorizationSettled  PrepaidAuthorizationState = "settled"
	PrepaidAuthorizationReleased PrepaidAuthorizationState = "released"
)

type PrepaidAuthorizationRequest struct {
	Owner               Owner
	ActionID            string
	RouterRequestID     string
	APIKeyID            string
	RequestedModel      string
	UpperBoundUsdMicros int64
}

type PrepaidAuthorization struct {
	Owner             Owner
	ActionID          string
	RouterRequestID   string
	APIKeyID          string
	RequestedModel    string
	ReservedUsdMicros int64
	SettledUsdMicros  int64
	State             PrepaidAuthorizationState
	CapacitySource    entitlement.CapacitySource
}

type PrepaidSettlement struct {
	AuthorizationActionID string
	ActionID              string
	RouterRequestID       string
	ServedModel           string
	RetailUsdMicros       int64
	CapacitySource        entitlement.CapacitySource
}

type SubscriberPrepaidAuthorizer interface {
	Authorize(ctx context.Context, request PrepaidAuthorizationRequest) (PrepaidAuthorization, error)
	Settle(ctx context.Context, settlement PrepaidSettlement) (balanceAfterMicros int64, err error)
	Finalize(ctx context.Context, actionID string) (balanceAfterMicros int64, err error)
}

type prepaidAuthorizationContextKey struct{}

type prepaidAuthorizationBinding struct {
	authorization PrepaidAuthorization
	actions       atomic.Int64
	failed        atomic.Bool
}

func WithPrepaidAuthorization(ctx context.Context, authorization PrepaidAuthorization) context.Context {
	return context.WithValue(ctx, prepaidAuthorizationContextKey{}, &prepaidAuthorizationBinding{authorization: authorization})
}

func PrepaidAuthorizationFromContext(ctx context.Context) (PrepaidAuthorization, bool) {
	binding, ok := ctx.Value(prepaidAuthorizationContextKey{}).(*prepaidAuthorizationBinding)
	if !ok {
		return PrepaidAuthorization{}, false
	}
	return binding.authorization, true
}

func NextPrepaidSettlementActionID(ctx context.Context, routerRequestID string) (string, bool) {
	binding, ok := ctx.Value(prepaidAuthorizationContextKey{}).(*prepaidAuthorizationBinding)
	if !ok || routerRequestID == "" {
		return "", false
	}
	return fmt.Sprintf("%s:%d", routerRequestID, binding.actions.Add(1)), true
}

func MarkPrepaidSettlementFailed(ctx context.Context) {
	if binding, ok := ctx.Value(prepaidAuthorizationContextKey{}).(*prepaidAuthorizationBinding); ok {
		binding.failed.Store(true)
	}
}

func PrepaidSettlementFailed(ctx context.Context) bool {
	binding, ok := ctx.Value(prepaidAuthorizationContextKey{}).(*prepaidAuthorizationBinding)
	return ok && binding.failed.Load()
}
