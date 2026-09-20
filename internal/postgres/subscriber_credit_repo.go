package postgres

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SubscriberCreditRepo is the prepaid book of an individual Max/Boost
// subscriber, keyed by credential subject. Every statement it issues carries
// that subject id, so colleagues who share an organization never share funds.
type SubscriberCreditRepo struct {
	queries *sqlc.Queries
}

// NewSubscriberCreditRepo binds subscriber prepaid funds to a SQLC handle.
func NewSubscriberCreditRepo(db sqlc.DBTX) *SubscriberCreditRepo {
	return &SubscriberCreditRepo{queries: sqlc.New(db)}
}

var _ billing.PrepaidBook = (*SubscriberCreditRepo)(nil)

// Balance returns the subscriber's prepaid balance in USD micros, mapping a
// missing row to billing.ErrBalanceRowMissing so a subscriber who never
// topped up reads the same as a depleted one does for the organization book.
func (r *SubscriberCreditRepo) Balance(ctx context.Context, owner billing.Owner) (int64, error) {
	subscriberID, err := subscriberOwnerID(owner)
	if err != nil {
		return 0, err
	}
	balance, err := r.queries.GetSubscriberCreditBalance(ctx, subscriberID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, billing.ErrBalanceRowMissing
	}
	if err != nil {
		return 0, fmt.Errorf("get subscriber credit balance: %w", err)
	}
	return balance, nil
}

// Debit moves the subscriber's balance and appends their ledger row in one
// statement. A BYOK fee has no meaning on an individual plan, so a debit that
// carries one is refused rather than silently dropping the fee row.
func (r *SubscriberCreditRepo) Debit(ctx context.Context, debit billing.PrepaidDebit) (int64, error) {
	subscriberID, err := subscriberOwnerID(debit.Owner)
	if err != nil {
		return 0, err
	}
	if debit.FeeUsdMicros != 0 {
		return 0, fmt.Errorf("%w: subscriber prepaid debits carry no fee row", billing.ErrInvalidOwner)
	}
	balanceAfter, err := r.queries.DebitSubscriberCredits(ctx, sqlc.DebitSubscriberCreditsParams{
		SubscriberID:       subscriberID,
		DeltaUsdMicros:     debit.DeltaUsdMicros,
		NotionalCostMicros: debit.NotionalCostMicros,
		EntryType:          debit.EntryType,
		RouterRequestID:    stringPtrOrNil(debit.RouterRequestID),
		RouterModel:        stringPtrOrNil(debit.RouterModel),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, billing.ErrBalanceRowMissing
	}
	if err != nil {
		return 0, fmt.Errorf("debit subscriber credits: %w", err)
	}
	return balanceAfter, nil
}

// subscriberOwnerID rejects any owner that is not a well-formed subscriber, so
// an organization id can never be reinterpreted as a credential subject.
func subscriberOwnerID(owner billing.Owner) (uuid.UUID, error) {
	if err := owner.Validate(); err != nil {
		return uuid.Nil, err
	}
	if owner.Kind != billing.OwnerKindSubscriber {
		return uuid.Nil, fmt.Errorf("%w: subscriber book cannot serve %q funds", billing.ErrOwnerKindUnsupported, owner.Kind)
	}
	subscriberID, err := uuid.Parse(owner.SubscriberID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: subscriber id %q", billing.ErrInvalidOwner, owner.SubscriberID)
	}
	return subscriberID, nil
}
