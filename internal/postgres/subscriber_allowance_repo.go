package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/subscriptions/entitlement"
)

// AllowanceDB is the database handle allowance accounting needs: a limit-
// enforcing reservation writes the action and every window accrual as one unit,
// so it cannot be expressed as a single statement over a plain handle.
type AllowanceDB interface {
	sqlc.DBTX
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// SubscriberAllowanceRepo accounts subscriber allowance holds and settlements.
type SubscriberAllowanceRepo struct {
	db      AllowanceDB
	queries *sqlc.Queries
}

// NewSubscriberAllowanceRepo binds allowance accounting to a SQLC database handle.
func NewSubscriberAllowanceRepo(db AllowanceDB) *SubscriberAllowanceRepo {
	return &SubscriberAllowanceRepo{db: db, queries: sqlc.New(db)}
}

// Reserve holds an upper-bound retail cost against every enforcement window.
// A redelivered action identifier returns the stored action without holding twice.
func (r *SubscriberAllowanceRepo) Reserve(ctx context.Context, reservation entitlement.Reservation) (entitlement.Action, error) {
	if err := reservation.Validate(); err != nil {
		return entitlement.Action{}, err
	}
	subscriberID, err := uuid.Parse(string(reservation.SubscriberID))
	if err != nil {
		return entitlement.Action{}, entitlement.ErrInvalidContract
	}
	apiKeyID, err := uuid.Parse(reservation.APIKeyID)
	if err != nil {
		return entitlement.Action{}, entitlement.ErrInvalidContract
	}
	row, err := r.queries.ReserveSubscriberAllowance(ctx, sqlc.ReserveSubscriberAllowanceParams{
		ActionID:              reservation.ActionID,
		RouterRequestID:       reservation.RouterRequestID,
		SubscriberID:          subscriberID,
		EntitlementVersion:    reservation.EntitlementVersion,
		Plan:                  string(reservation.Plan),
		BillingPeriodStart:    utcTimestamptz(reservation.BillingPeriod.Start),
		BillingPeriodEnd:      utcTimestamptz(reservation.BillingPeriod.End),
		WeeklyPeriodStart:     utcTimestamptz(reservation.WeeklyPeriod.Start),
		WeeklyPeriodEnd:       utcTimestamptz(reservation.WeeklyPeriod.End),
		SixHourPeriodStart:    utcTimestamptz(reservation.SixHourPeriod.Start),
		SixHourPeriodEnd:      utcTimestamptz(reservation.SixHourPeriod.End),
		APIKeyID:              apiKeyID,
		ClientSessionID:       optionalText(reservation.ClientSessionID),
		RequestedModel:        reservation.RequestedModel,
		ReservedUsdMicros:     reservation.ReservedUsdMicros,
		CapacitySource:        string(reservation.CapacitySource),
		ReservedAt:            utcTimestamptz(reservation.ReservedAt),
		BillingLimitUsdMicros: reservation.BillingLimitUsdMicros,
		WeeklyLimitUsdMicros:  reservation.WeeklyLimitUsdMicros,
		SixHourLimitUsdMicros: reservation.SixHourLimitUsdMicros,
	})
	stored, err := r.decodeOrReread(ctx, reservation.ActionID, sqlc.RouterSubscriberAllowanceAction(row), err)
	if err != nil {
		return entitlement.Action{}, err
	}
	if !sameReservation(stored.Reservation, reservation) {
		return entitlement.Action{}, entitlement.ErrAllowanceActionConflict
	}
	return stored, nil
}

// ReserveWithinLimits holds an upper-bound retail cost only while every
// enforcement window can still pay for it.
//
// Every window accrual is gated on the invariant inside the statement that
// performs it, so a concurrent reservation cannot slip between a check and its
// write: Postgres re-evaluates the gate against the latest committed row when
// the upsert conflicts. The windows are gated separately, so the whole
// reservation runs in a transaction — an accrual against the month that the
// six-hour window then refuses must not stand.
func (r *SubscriberAllowanceRepo) ReserveWithinLimits(ctx context.Context, reservation entitlement.Reservation) (entitlement.Action, error) {
	if err := reservation.Validate(); err != nil {
		return entitlement.Action{}, err
	}
	subscriberID, err := uuid.Parse(string(reservation.SubscriberID))
	if err != nil {
		return entitlement.Action{}, entitlement.ErrInvalidContract
	}
	apiKeyID, err := uuid.Parse(reservation.APIKeyID)
	if err != nil {
		return entitlement.Action{}, entitlement.ErrInvalidContract
	}
	var held entitlement.Action
	err = pgx.BeginTxFunc(ctx, r.db, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		row, insertErr := queries.InsertSubscriberAllowanceAction(ctx, sqlc.InsertSubscriberAllowanceActionParams{
			ActionID:           reservation.ActionID,
			RouterRequestID:    reservation.RouterRequestID,
			SubscriberID:       subscriberID,
			EntitlementVersion: reservation.EntitlementVersion,
			Plan:               string(reservation.Plan),
			BillingPeriodStart: utcTimestamptz(reservation.BillingPeriod.Start),
			BillingPeriodEnd:   utcTimestamptz(reservation.BillingPeriod.End),
			WeeklyPeriodStart:  utcTimestamptz(reservation.WeeklyPeriod.Start),
			WeeklyPeriodEnd:    utcTimestamptz(reservation.WeeklyPeriod.End),
			SixHourPeriodStart: utcTimestamptz(reservation.SixHourPeriod.Start),
			SixHourPeriodEnd:   utcTimestamptz(reservation.SixHourPeriod.End),
			APIKeyID:           apiKeyID,
			ClientSessionID:    optionalText(reservation.ClientSessionID),
			RequestedModel:     reservation.RequestedModel,
			ReservedUsdMicros:  reservation.ReservedUsdMicros,
			CapacitySource:     string(reservation.CapacitySource),
			ReservedAt:         utcTimestamptz(reservation.ReservedAt),
		})
		if errors.Is(insertErr, pgx.ErrNoRows) {
			// Redelivery: the first delivery already drew the windows down.
			stored, readErr := readAllowanceAction(ctx, queries, reservation.ActionID)
			if readErr != nil {
				return readErr
			}
			if !sameReservation(stored.Reservation, reservation) {
				return entitlement.ErrAllowanceActionConflict
			}
			held = stored
			return nil
		}
		if insertErr != nil {
			return fmt.Errorf("hold subscriber allowance: %w", insertErr)
		}
		stored, decodeErr := toAllowanceAction(row)
		if decodeErr != nil {
			return decodeErr
		}
		if reservation.CapacitySource != entitlement.CapacitySourceIncludedRouter {
			// Linked and prepaid capacity is audited, never drawn from the
			// subscription's own allowance, so no window gates it.
			held = stored
			return nil
		}
		for _, kind := range []entitlement.PeriodKind{entitlement.PeriodKindBilling, entitlement.PeriodKindWeekly, entitlement.PeriodKindSixHour} {
			if accrueErr := accrueWindow(ctx, queries, subscriberID, reservation, kind); accrueErr != nil {
				return accrueErr
			}
		}
		held = stored
		return nil
	})
	if err != nil {
		return entitlement.Action{}, err
	}
	return held, nil
}

// accrueWindow draws one window down by the reservation, reporting the window
// as exhausted when the gate refuses the accrual.
func accrueWindow(ctx context.Context, queries *sqlc.Queries, subscriberID uuid.UUID, reservation entitlement.Reservation, kind entitlement.PeriodKind) error {
	period := reservation.BillingPeriod
	limit := reservation.BillingLimitUsdMicros
	switch kind {
	case entitlement.PeriodKindWeekly:
		period = reservation.WeeklyPeriod
		limit = reservation.WeeklyLimitUsdMicros
	case entitlement.PeriodKindSixHour:
		period = reservation.SixHourPeriod
		limit = reservation.SixHourLimitUsdMicros
	}
	_, err := queries.AccrueSubscriberAllowancePeriod(ctx, sqlc.AccrueSubscriberAllowancePeriodParams{
		SubscriberID:       subscriberID,
		PeriodKind:         string(kind),
		PeriodStart:        utcTimestamptz(period.Start),
		PeriodEnd:          utcTimestamptz(period.End),
		EntitlementVersion: reservation.EntitlementVersion,
		Plan:               string(reservation.Plan),
		LimitUsdMicros:     limit,
		ReservedUsdMicros:  reservation.ReservedUsdMicros,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return entitlement.ExhaustedError{Period: kind}
	}
	if err != nil {
		return fmt.Errorf("accrue subscriber allowance window: %w", err)
	}
	return nil
}

func readAllowanceAction(ctx context.Context, queries *sqlc.Queries, actionID string) (entitlement.Action, error) {
	row, err := queries.GetSubscriberAllowanceAction(ctx, actionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return entitlement.Action{}, entitlement.ErrAllowanceActionNotFound
	}
	if err != nil {
		return entitlement.Action{}, fmt.Errorf("read subscriber allowance action: %w", err)
	}
	return toAllowanceAction(row)
}

// Finalize settles a held action at its actual retail cost.
func (r *SubscriberAllowanceRepo) Finalize(ctx context.Context, finalization entitlement.Finalization) (entitlement.Action, error) {
	if err := finalization.Validate(); err != nil {
		return entitlement.Action{}, err
	}
	row, err := r.queries.FinalizeSubscriberAllowance(ctx, sqlc.FinalizeSubscriberAllowanceParams{
		ActionID:        finalization.ActionID,
		ServedModel:     finalization.ServedModel,
		RetailUsdMicros: finalization.RetailUsdMicros,
		CapacitySource:  string(finalization.CapacitySource),
		FinalizedAt:     utcTimestamptz(finalization.FinalizedAt),
	})
	stored, err := r.decodeOrReread(ctx, finalization.ActionID, sqlc.RouterSubscriberAllowanceAction(row), err)
	if err != nil {
		return entitlement.Action{}, err
	}
	stored, err = r.rereadUntransitioned(ctx, stored)
	if err != nil {
		return entitlement.Action{}, err
	}
	if stored.State != entitlement.ActionStateFinalized || stored.ServedModel != finalization.ServedModel ||
		stored.RetailUsdMicros != finalization.RetailUsdMicros || stored.CapacitySource != finalization.CapacitySource {
		return entitlement.Action{}, entitlement.ErrAllowanceActionConflict
	}
	return stored, nil
}

// Release returns a non-billable hold to every enforcement window.
func (r *SubscriberAllowanceRepo) Release(ctx context.Context, release entitlement.Release) (entitlement.Action, error) {
	if err := release.Validate(); err != nil {
		return entitlement.Action{}, err
	}
	row, err := r.queries.ReleaseSubscriberAllowance(ctx, sqlc.ReleaseSubscriberAllowanceParams{
		ActionID:   release.ActionID,
		ReleasedAt: utcTimestamptz(release.ReleasedAt),
	})
	stored, err := r.decodeOrReread(ctx, release.ActionID, sqlc.RouterSubscriberAllowanceAction(row), err)
	if err != nil {
		return entitlement.Action{}, err
	}
	stored, err = r.rereadUntransitioned(ctx, stored)
	if err != nil {
		return entitlement.Action{}, err
	}
	if stored.State != entitlement.ActionStateReleased {
		return entitlement.Action{}, entitlement.ErrAllowanceActionConflict
	}
	return stored, nil
}

// Usage reads consumption of the enforcement windows covering one request.
// A window with no activity yet reports zero consumption rather than an error.
func (r *SubscriberAllowanceRepo) Usage(ctx context.Context, subscriberID entitlement.SubscriberID, billing, weekly, sixHour entitlement.Period) (entitlement.Usage, error) {
	if billing.Kind != entitlement.PeriodKindBilling || weekly.Kind != entitlement.PeriodKindWeekly || sixHour.Kind != entitlement.PeriodKindSixHour {
		return entitlement.Usage{}, entitlement.ErrInvalidContract
	}
	for _, period := range []entitlement.Period{billing, weekly, sixHour} {
		if err := period.Validate(); err != nil {
			return entitlement.Usage{}, err
		}
	}
	id, err := uuid.Parse(string(subscriberID))
	if err != nil {
		return entitlement.Usage{}, entitlement.ErrInvalidContract
	}
	rows, err := r.queries.ListSubscriberAllowanceWindows(ctx, sqlc.ListSubscriberAllowanceWindowsParams{
		SubscriberID:       id,
		BillingPeriodStart: utcTimestamptz(billing.Start),
		WeeklyPeriodStart:  utcTimestamptz(weekly.Start),
		SixHourPeriodStart: utcTimestamptz(sixHour.Start),
	})
	if err != nil {
		return entitlement.Usage{}, fmt.Errorf("read subscriber allowance windows: %w", err)
	}
	usage := entitlement.Usage{
		Billing: entitlement.WindowUsage{Period: billing},
		Weekly:  entitlement.WindowUsage{Period: weekly},
		SixHour: entitlement.WindowUsage{Period: sixHour},
	}
	for _, row := range rows {
		window := entitlement.WindowUsage{
			Period: entitlement.Period{
				Kind:  entitlement.PeriodKind(row.PeriodKind),
				Start: subscriberTimestamptzUTCOrZero(row.PeriodStart),
				End:   subscriberTimestamptzUTCOrZero(row.PeriodEnd),
			},
			LimitUsdMicros:     row.LimitUsdMicros,
			ReservedUsdMicros:  row.ReservedUsdMicros,
			FinalizedUsdMicros: row.FinalizedUsdMicros,
		}
		switch window.Period.Kind {
		case entitlement.PeriodKindBilling:
			usage.Billing = window
		case entitlement.PeriodKindWeekly:
			usage.Weekly = window
		case entitlement.PeriodKindSixHour:
			usage.SixHour = window
		}
	}
	return usage, nil
}

// decodeOrReread turns the result of a write statement into the stored action.
// A statement that lost to a concurrent delivery of the same action returns no
// row at all: its insert or update sees the conflict through the index, but its
// own snapshot predates the winner's commit, so the fallback read inside the
// statement cannot see the row. A separate read gets fresh visibility.
func (r *SubscriberAllowanceRepo) decodeOrReread(ctx context.Context, actionID string, row sqlc.RouterSubscriberAllowanceAction, err error) (entitlement.Action, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return r.readAction(ctx, actionID)
	}
	if err != nil {
		return entitlement.Action{}, fmt.Errorf("account subscriber allowance: %w", err)
	}
	return toAllowanceAction(row)
}

// rereadUntransitioned re-reads an action that a settlement statement left
// reserved, so a transition committed by a concurrent delivery is recognized as
// the redelivery it is rather than reported as a conflict.
func (r *SubscriberAllowanceRepo) rereadUntransitioned(ctx context.Context, stored entitlement.Action) (entitlement.Action, error) {
	if stored.State != entitlement.ActionStateReserved {
		return stored, nil
	}
	return r.readAction(ctx, stored.ActionID)
}

func (r *SubscriberAllowanceRepo) readAction(ctx context.Context, actionID string) (entitlement.Action, error) {
	return readAllowanceAction(ctx, r.queries, actionID)
}

// sameReservation reports whether a redelivery carries the identity of the hold
// already stored under that action identifier. The window limits, the
// reservation clock, and the windows derived from it are excluded: they seed
// and key the period rows the hold already accrued against, and a retry that
// arrives after a window boundary legitimately carries later ones.
func sameReservation(stored, redelivered entitlement.Reservation) bool {
	return stored.RouterRequestID == redelivered.RouterRequestID &&
		stored.SubscriberID == redelivered.SubscriberID &&
		stored.EntitlementVersion == redelivered.EntitlementVersion &&
		stored.Plan == redelivered.Plan &&
		stored.APIKeyID == redelivered.APIKeyID &&
		stored.ClientSessionID == redelivered.ClientSessionID &&
		stored.RequestedModel == redelivered.RequestedModel &&
		stored.ReservedUsdMicros == redelivered.ReservedUsdMicros &&
		stored.CapacitySource == redelivered.CapacitySource
}

func toAllowanceAction(row sqlc.RouterSubscriberAllowanceAction) (entitlement.Action, error) {
	action := entitlement.Action{
		Reservation: entitlement.Reservation{
			ActionID:           row.ActionID,
			RouterRequestID:    row.RouterRequestID,
			SubscriberID:       entitlement.SubscriberID(row.SubscriberID.String()),
			EntitlementVersion: row.EntitlementVersion,
			Plan:               entitlement.Plan(row.Plan),
			BillingPeriod: entitlement.Period{
				Kind:  entitlement.PeriodKindBilling,
				Start: subscriberTimestamptzUTCOrZero(row.BillingPeriodStart),
				End:   subscriberTimestamptzUTCOrZero(row.BillingPeriodEnd),
			},
			WeeklyPeriod: entitlement.Period{
				Kind:  entitlement.PeriodKindWeekly,
				Start: subscriberTimestamptzUTCOrZero(row.WeeklyPeriodStart),
				End:   subscriberTimestamptzUTCOrZero(row.WeeklyPeriodEnd),
			},
			SixHourPeriod: entitlement.Period{
				Kind:  entitlement.PeriodKindSixHour,
				Start: subscriberTimestamptzUTCOrZero(row.SixHourPeriodStart),
				End:   subscriberTimestamptzUTCOrZero(row.SixHourPeriodEnd),
			},
			APIKeyID:          row.APIKeyID.String(),
			ClientSessionID:   textOrEmpty(row.ClientSessionID),
			RequestedModel:    row.RequestedModel,
			ReservedUsdMicros: row.ReservedUsdMicros,
			CapacitySource:    entitlement.CapacitySource(row.CapacitySource),
			ReservedAt:        subscriberTimestamptzUTCOrZero(row.ReservedAt),
		},
		State:           entitlement.ActionState(row.State),
		ServedModel:     textOrEmpty(row.ServedModel),
		RetailUsdMicros: bigintOrZero(row.RetailUsdMicros),
		FinalizedAt:     optionalUTCTime(row.FinalizedAt),
		ReleasedAt:      optionalUTCTime(row.ReleasedAt),
	}
	if err := action.Validate(); err != nil {
		return entitlement.Action{}, fmt.Errorf("decode subscriber allowance action: %w", err)
	}
	return action, nil
}

func utcTimestamptz(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func textOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func bigintOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func optionalUTCTime(value pgtype.Timestamptz) *time.Time {
	timestamp := subscriberTimestamptzUTCOrZero(value)
	if timestamp.IsZero() {
		return nil
	}
	return &timestamp
}

var _ entitlement.AllowanceRepository = (*SubscriberAllowanceRepo)(nil)
