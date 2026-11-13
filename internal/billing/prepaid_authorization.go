package billing

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"weave-os/router/internal/subscriptions/entitlement"
)

var (
	// ErrPrepaidAuthorizationConflict means an idempotency key was reused with different details.
	ErrPrepaidAuthorizationConflict = errors.New("billing: conflicting prepaid authorization")
	// ErrPrepaidAuthorizationNotFound means settlement referenced no durable authorization.
	ErrPrepaidAuthorizationNotFound = errors.New("billing: prepaid authorization not found")
)

// PrepaidAuthorizationState is the durable lifecycle of a subscriber prepaid hold.
type PrepaidAuthorizationState string

const (
	// PrepaidAuthorizationReserved means funds remain held for a request.
	PrepaidAuthorizationReserved PrepaidAuthorizationState = "reserved"
	// PrepaidAuthorizationSettled means served actions consumed some or all of the hold.
	PrepaidAuthorizationSettled PrepaidAuthorizationState = "settled"
	// PrepaidAuthorizationReleased means no billable action consumed the hold.
	PrepaidAuthorizationReleased PrepaidAuthorizationState = "released"
)

// PrepaidAuthorizationRequest requests a bounded hold before provider dispatch.
type PrepaidAuthorizationRequest struct {
	Owner               Owner
	ActionID            string
	RouterRequestID     string
	APIKeyID            string
	RequestedModel      string
	UpperBoundUsdMicros int64
}

// PrepaidAuthorization is a durable hold against one subscriber's funds.
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

// PrepaidSettlement records one served action's exact retail cost.
type PrepaidSettlement struct {
	AuthorizationActionID string
	ActionID              string
	RouterRequestID       string
	ServedModel           string
	RetailUsdMicros       int64
	CapacitySource        entitlement.CapacitySource
}

// SubscriberPrepaidAuthorizer reserves, settles, and releases subscriber funds.
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

// WithPrepaidAuthorization stamps the authorized subscriber hold onto a request.
func WithPrepaidAuthorization(ctx context.Context, authorization PrepaidAuthorization) context.Context {
	return context.WithValue(ctx, prepaidAuthorizationContextKey{}, &prepaidAuthorizationBinding{authorization: authorization})
}

// PrepaidAuthorizationFromContext returns the subscriber hold selected at admission.
func PrepaidAuthorizationFromContext(ctx context.Context) (PrepaidAuthorization, bool) {
	binding, ok := ctx.Value(prepaidAuthorizationContextKey{}).(*prepaidAuthorizationBinding)
	if !ok {
		return PrepaidAuthorization{}, false
	}
	return binding.authorization, true
}

// NextPrepaidSettlementActionID derives a stable per-action settlement identifier.
func NextPrepaidSettlementActionID(ctx context.Context, routerRequestID string) (string, bool) {
	binding, ok := ctx.Value(prepaidAuthorizationContextKey{}).(*prepaidAuthorizationBinding)
	if !ok || routerRequestID == "" {
		return "", false
	}
	return fmt.Sprintf("%s:%d", routerRequestID, binding.actions.Add(1)), true
}

// MarkPrepaidSettlementFailed preserves the hold for reconciliation.
func MarkPrepaidSettlementFailed(ctx context.Context) {
	if binding, ok := ctx.Value(prepaidAuthorizationContextKey{}).(*prepaidAuthorizationBinding); ok {
		binding.failed.Store(true)
	}
}

// PrepaidSettlementFailed reports whether a served action failed to settle.
func PrepaidSettlementFailed(ctx context.Context) bool {
	binding, ok := ctx.Value(prepaidAuthorizationContextKey{}).(*prepaidAuthorizationBinding)
	return ok && binding.failed.Load()
}
