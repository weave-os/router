package postgres

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type subscriberCreditDB interface {
	sqlc.DBTX
	Begin(context.Context) (pgx.Tx, error)
}

// SubscriberCreditRepo is the prepaid book of an individual Max/Boost
// subscriber, keyed by credential subject. Every statement it issues carries
// that subject id, so colleagues who share an organization never share funds.
type SubscriberCreditRepo struct {
	db      subscriberCreditDB
	queries *sqlc.Queries
}

// NewSubscriberCreditRepo binds subscriber prepaid funds to a SQLC handle.
func NewSubscriberCreditRepo(db subscriberCreditDB) *SubscriberCreditRepo {
	boundedDB := dbbudget.NewDBTX(db)
	return &SubscriberCreditRepo{db: boundedDB, queries: dbbudget.Queries(boundedDB)}
}

var _ billing.PrepaidBook = (*SubscriberCreditRepo)(nil)
var _ billing.SubscriberPrepaidAuthorizer = (*SubscriberCreditRepo)(nil)

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

// Authorize reserves at most the request bound and never more than the
// subscriber's current balance.
func (r *SubscriberCreditRepo) Authorize(ctx context.Context, request billing.PrepaidAuthorizationRequest) (billing.PrepaidAuthorization, error) {
	subscriberID, err := subscriberOwnerID(request.Owner)
	if err != nil {
		return billing.PrepaidAuthorization{}, err
	}
	if request.ActionID == "" || request.RouterRequestID == "" || request.RequestedModel == "" || request.UpperBoundUsdMicros <= 0 {
		return billing.PrepaidAuthorization{}, billing.ErrPrepaidAuthorizationConflict
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return billing.PrepaidAuthorization{}, fmt.Errorf("begin subscriber credit authorization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbbudget.Queries(tx)

	var reserved int64
	for {
		stored, readErr := queries.GetSubscriberCreditReservation(ctx, request.ActionID)
		if readErr == nil {
			authorization, decodeErr := decodeSubscriberCreditReservation(stored)
			if decodeErr != nil {
				return billing.PrepaidAuthorization{}, decodeErr
			}
			if !sameSubscriberAuthorization(authorization, request) {
				return billing.PrepaidAuthorization{}, billing.ErrPrepaidAuthorizationConflict
			}
			return authorization, nil
		}
		if !errors.Is(readErr, pgx.ErrNoRows) {
			return billing.PrepaidAuthorization{}, fmt.Errorf("read subscriber credit authorization: %w", readErr)
		}
		balance, balanceErr := queries.GetSubscriberCreditBalance(ctx, subscriberID)
		if errors.Is(balanceErr, pgx.ErrNoRows) {
			return billing.PrepaidAuthorization{}, billing.ErrBalanceRowMissing
		}
		if balanceErr != nil {
			return billing.PrepaidAuthorization{}, fmt.Errorf("read subscriber credit balance: %w", balanceErr)
		}
		if balance <= 0 {
			stored, readErr := queries.GetSubscriberCreditReservation(ctx, request.ActionID)
			if readErr == nil {
				authorization, decodeErr := decodeSubscriberCreditReservation(stored)
				if decodeErr != nil {
					return billing.PrepaidAuthorization{}, decodeErr
				}
				if !sameSubscriberAuthorization(authorization, request) {
					return billing.PrepaidAuthorization{}, billing.ErrPrepaidAuthorizationConflict
				}
				return authorization, nil
			}
			if !errors.Is(readErr, pgx.ErrNoRows) {
				return billing.PrepaidAuthorization{}, fmt.Errorf("read subscriber credit authorization replay: %w", readErr)
			}
			return billing.PrepaidAuthorization{}, billing.ErrInsufficientCredits
		}
		reserved = min(request.UpperBoundUsdMicros, balance)
		_, reserveErr := queries.ReserveSubscriberCredits(ctx, sqlc.ReserveSubscriberCreditsParams{
			ReservedUsdMicros: reserved,
			SubscriberID:      subscriberID,
		})
		if reserveErr == nil {
			break
		}
		if !errors.Is(reserveErr, pgx.ErrNoRows) {
			return billing.PrepaidAuthorization{}, fmt.Errorf("reserve subscriber credits: %w", reserveErr)
		}
	}
	stored, err := queries.InsertSubscriberCreditReservation(ctx, sqlc.InsertSubscriberCreditReservationParams{
		ActionID:          request.ActionID,
		SubscriberID:      subscriberID,
		RouterRequestID:   request.RouterRequestID,
		APIKeyID:          stringPtrOrNil(request.APIKeyID),
		RequestedModel:    request.RequestedModel,
		ReservedUsdMicros: reserved,
		CapacitySource:    string(entitlement.CapacitySourcePrepaid),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			existing, readErr := r.queries.GetSubscriberCreditReservation(ctx, request.ActionID)
			if readErr != nil {
				return billing.PrepaidAuthorization{}, fmt.Errorf("read subscriber credit authorization replay: %w", readErr)
			}
			authorization, decodeErr := decodeSubscriberCreditReservation(existing)
			if decodeErr != nil {
				return billing.PrepaidAuthorization{}, decodeErr
			}
			if !sameSubscriberAuthorization(authorization, request) {
				return billing.PrepaidAuthorization{}, billing.ErrPrepaidAuthorizationConflict
			}
			return authorization, nil
		}
		return billing.PrepaidAuthorization{}, fmt.Errorf("insert subscriber credit authorization: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return billing.PrepaidAuthorization{}, fmt.Errorf("commit subscriber credit authorization: %w", err)
	}
	return decodeSubscriberCreditReservation(stored)
}

// Settle records one exact retail-cost action against an open authorization.
func (r *SubscriberCreditRepo) Settle(ctx context.Context, settlement billing.PrepaidSettlement) (int64, error) {
	if settlement.AuthorizationActionID == "" || settlement.ActionID == "" || settlement.RouterRequestID == "" ||
		settlement.ServedModel == "" || settlement.RetailUsdMicros < 0 ||
		settlement.CapacitySource != entitlement.CapacitySourcePrepaid {
		return 0, billing.ErrPrepaidAuthorizationConflict
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin subscriber credit settlement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbbudget.Queries(tx)

	reservation, err := queries.GetSubscriberCreditReservation(ctx, settlement.AuthorizationActionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, billing.ErrPrepaidAuthorizationNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read subscriber credit authorization: %w", err)
	}

	storedSettlement, err := queries.GetSubscriberCreditSettlement(ctx, settlement.ActionID)
	if err == nil {
		if !sameSubscriberSettlement(storedSettlement, reservation, settlement) {
			return 0, billing.ErrPrepaidAuthorizationConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("commit subscriber credit settlement replay: %w", err)
		}
		return storedSettlement.BalanceAfterMicros, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("read subscriber credit settlement: %w", err)
	}
	updatedReservation, err := queries.AddSubscriberCreditReservationSettlement(ctx, sqlc.AddSubscriberCreditReservationSettlementParams{
		RetailUsdMicros: settlement.RetailUsdMicros,
		ActionID:        settlement.AuthorizationActionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, readErr := queries.GetSubscriberCreditSettlement(ctx, settlement.ActionID)
		if readErr == nil {
			if !sameSubscriberSettlement(existing, reservation, settlement) {
				return 0, billing.ErrPrepaidAuthorizationConflict
			}
			return existing.BalanceAfterMicros, nil
		}
		if !errors.Is(readErr, pgx.ErrNoRows) {
			return 0, fmt.Errorf("read subscriber credit settlement replay: %w", readErr)
		}
		return 0, billing.ErrPrepaidAuthorizationConflict
	}
	if err != nil {
		return 0, fmt.Errorf("advance subscriber credit authorization: %w", err)
	}
	reservation = updatedReservation
	remainingHold := max(reservation.ReservedUsdMicros-(reservation.SettledUsdMicros-settlement.RetailUsdMicros), 0)
	overage := max(settlement.RetailUsdMicros-remainingHold, 0)
	balance, err := queries.UpdateSubscriberCreditBalance(ctx, sqlc.UpdateSubscriberCreditBalanceParams{
		DeltaUsdMicros: -overage,
		SubscriberID:   reservation.SubscriberID,
	})
	if err != nil {
		return 0, fmt.Errorf("debit subscriber credit settlement overage: %w", err)
	}
	balanceAfter := balance + max(reservation.ReservedUsdMicros-reservation.SettledUsdMicros, 0)
	if _, err := queries.InsertSubscriberCreditSettlement(ctx, sqlc.InsertSubscriberCreditSettlementParams{
		SubscriberID:          reservation.SubscriberID,
		DebitUsdMicros:        -settlement.RetailUsdMicros,
		RetailUsdMicros:       settlement.RetailUsdMicros,
		BalanceAfterMicros:    balanceAfter,
		RouterRequestID:       settlement.RouterRequestID,
		RouterModel:           settlement.ServedModel,
		AuthorizationActionID: settlement.AuthorizationActionID,
		ActionID:              settlement.ActionID,
		CapacitySource:        string(settlement.CapacitySource),
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			existing, readErr := r.queries.GetSubscriberCreditSettlement(ctx, settlement.ActionID)
			if readErr != nil {
				return 0, fmt.Errorf("read subscriber credit settlement replay: %w", readErr)
			}
			if !sameSubscriberSettlement(existing, reservation, settlement) {
				return 0, billing.ErrPrepaidAuthorizationConflict
			}
			return existing.BalanceAfterMicros, nil
		}
		return 0, fmt.Errorf("insert subscriber credit settlement: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit subscriber credit settlement: %w", err)
	}
	return balanceAfter, nil
}

// Finalize returns any unused hold after all served actions have settled.
func (r *SubscriberCreditRepo) Finalize(ctx context.Context, actionID string) (int64, error) {
	if actionID == "" {
		return 0, billing.ErrPrepaidAuthorizationConflict
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin subscriber credit finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbbudget.Queries(tx)

	reservation, err := queries.FinalizeSubscriberCreditReservation(ctx, actionID)
	if errors.Is(err, pgx.ErrNoRows) {
		reservation, readErr := queries.GetSubscriberCreditReservation(ctx, actionID)
		if errors.Is(readErr, pgx.ErrNoRows) {
			return 0, billing.ErrPrepaidAuthorizationNotFound
		}
		if readErr != nil {
			return 0, fmt.Errorf("read subscriber credit finalization replay: %w", readErr)
		}
		balance, balanceErr := queries.GetSubscriberCreditBalance(ctx, reservation.SubscriberID)
		if balanceErr != nil {
			return 0, fmt.Errorf("read subscriber credit balance for finalization replay: %w", balanceErr)
		}
		return balance, nil
	}
	if err != nil {
		return 0, fmt.Errorf("finalize subscriber credit authorization: %w", err)
	}

	unused := max(reservation.ReservedUsdMicros-reservation.SettledUsdMicros, 0)
	balance, err := queries.UpdateSubscriberCreditBalance(ctx, sqlc.UpdateSubscriberCreditBalanceParams{
		DeltaUsdMicros: unused,
		SubscriberID:   reservation.SubscriberID,
	})
	if err != nil {
		return 0, fmt.Errorf("return unused subscriber credit hold: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit subscriber credit finalization: %w", err)
	}
	return balance, nil
}

func decodeSubscriberCreditReservation(stored sqlc.RouterSubscriberCreditReservation) (billing.PrepaidAuthorization, error) {
	authorization := billing.PrepaidAuthorization{
		Owner:             billing.SubscriberOwner(stored.SubscriberID.String()),
		ActionID:          stored.ActionID,
		RouterRequestID:   stored.RouterRequestID,
		APIKeyID:          stringOrEmpty(stored.APIKeyID),
		RequestedModel:    stored.RequestedModel,
		ReservedUsdMicros: stored.ReservedUsdMicros,
		SettledUsdMicros:  stored.SettledUsdMicros,
		State:             billing.PrepaidAuthorizationState(stored.State),
		CapacitySource:    entitlement.CapacitySource(stored.CapacitySource),
	}
	if authorization.ActionID == "" || authorization.ReservedUsdMicros <= 0 ||
		authorization.CapacitySource != entitlement.CapacitySourcePrepaid {
		return billing.PrepaidAuthorization{}, billing.ErrPrepaidAuthorizationConflict
	}
	return authorization, nil
}

func sameSubscriberAuthorization(stored billing.PrepaidAuthorization, request billing.PrepaidAuthorizationRequest) bool {
	return stored.Owner == request.Owner &&
		stored.ActionID == request.ActionID &&
		stored.RouterRequestID == request.RouterRequestID &&
		stored.APIKeyID == request.APIKeyID &&
		stored.RequestedModel == request.RequestedModel &&
		stored.ReservedUsdMicros <= request.UpperBoundUsdMicros
}

func sameSubscriberSettlement(stored sqlc.RouterSubscriberCreditLedger, reservation sqlc.RouterSubscriberCreditReservation, settlement billing.PrepaidSettlement) bool {
	return stored.SubscriberID == reservation.SubscriberID &&
		stored.DeltaUsdMicros == -settlement.RetailUsdMicros &&
		stored.NotionalCostMicros == settlement.RetailUsdMicros &&
		stringOrEmpty(stored.RouterRequestID) == settlement.RouterRequestID &&
		stringOrEmpty(stored.RouterModel) == settlement.ServedModel &&
		stringOrEmpty(stored.AuthorizationActionID) == settlement.AuthorizationActionID &&
		stringOrEmpty(stored.ActionID) == settlement.ActionID &&
		stringOrEmpty(stored.CapacitySource) == string(settlement.CapacitySource)
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
