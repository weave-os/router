package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/subscriptions/entitlement"
)

type subscriberAllowanceDB struct {
	actionRows []subscriberAllowanceActionRow
	// singleRows answers QueryRow ahead of actionRows, so a transactional
	// reservation can queue an action row followed by its window accruals.
	singleRows []pgx.Row
	periodRows []sqlc.RouterSubscriberAllowancePeriod
	queryErr   error
	args       [][]any
	committed  int
	rolledBack int
}

// subscriberAllowanceTx runs the reservation's statements against the same
// recorded rows, and reports whether the unit committed or unwound.
type subscriberAllowanceTx struct {
	pgx.Tx
	db *subscriberAllowanceDB
}

func (db *subscriberAllowanceDB) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return &subscriberAllowanceTx{db: db}, nil
}

func (tx *subscriberAllowanceTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return tx.db.Exec(ctx, sql, args...)
}

func (tx *subscriberAllowanceTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return tx.db.Query(ctx, sql, args...)
}

func (tx *subscriberAllowanceTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return tx.db.QueryRow(ctx, sql, args...)
}

func (tx *subscriberAllowanceTx) Commit(context.Context) error {
	tx.db.committed++
	return nil
}

func (tx *subscriberAllowanceTx) Rollback(context.Context) error {
	tx.db.rolledBack++
	return nil
}

func (*subscriberAllowanceDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec call")
}

func (db *subscriberAllowanceDB) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	db.args = append(db.args, args)
	if db.queryErr != nil {
		return nil, db.queryErr
	}
	return &subscriberAllowancePeriodRows{remaining: db.periodRows}, nil
}

func (db *subscriberAllowanceDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	db.args = append(db.args, args)
	if len(db.singleRows) > 0 {
		row := db.singleRows[0]
		db.singleRows = db.singleRows[1:]
		return row
	}
	if len(db.actionRows) == 0 {
		return subscriberAllowanceActionRow{err: pgx.ErrNoRows}
	}
	row := db.actionRows[0]
	db.actionRows = db.actionRows[1:]
	return row
}

type subscriberAllowanceActionRow struct {
	value sqlc.RouterSubscriberAllowanceAction
	err   error
}

func (row subscriberAllowanceActionRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	action := row.value
	return assignScanTargets(dest, []any{
		action.ActionID,
		action.RouterRequestID,
		action.SubscriberID,
		action.EntitlementVersion,
		action.Plan,
		action.BillingPeriodStart,
		action.BillingPeriodEnd,
		action.SixHourPeriodStart,
		action.SixHourPeriodEnd,
		action.APIKeyID,
		action.ClientSessionID,
		action.RequestedModel,
		action.ServedModel,
		action.ReservedUsdMicros,
		action.RetailUsdMicros,
		action.CapacitySource,
		action.State,
		action.ReservedAt,
		action.FinalizedAt,
		action.ReleasedAt,
		action.CreatedAt,
		action.UpdatedAt,
	})
}

// subscriberAllowancePeriodRow answers one accrual: a value row when the
// window paid for the hold, pgx.ErrNoRows when its gate refused it.
type subscriberAllowancePeriodRow struct {
	value sqlc.RouterSubscriberAllowancePeriod
	err   error
}

func (row subscriberAllowancePeriodRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	period := row.value
	return assignScanTargets(dest, []any{
		period.SubscriberID,
		period.PeriodKind,
		period.PeriodStart,
		period.PeriodEnd,
		period.EntitlementVersion,
		period.Plan,
		period.LimitUsdMicros,
		period.ReservedUsdMicros,
		period.FinalizedUsdMicros,
		period.CreatedAt,
		period.UpdatedAt,
	})
}

type subscriberAllowancePeriodRows struct {
	remaining []sqlc.RouterSubscriberAllowancePeriod
	current   sqlc.RouterSubscriberAllowancePeriod
}

func (rows *subscriberAllowancePeriodRows) Next() bool {
	if len(rows.remaining) == 0 {
		return false
	}
	rows.current = rows.remaining[0]
	rows.remaining = rows.remaining[1:]
	return true
}

func (rows *subscriberAllowancePeriodRows) Scan(dest ...any) error {
	period := rows.current
	return assignScanTargets(dest, []any{
		period.SubscriberID,
		period.PeriodKind,
		period.PeriodStart,
		period.PeriodEnd,
		period.EntitlementVersion,
		period.Plan,
		period.LimitUsdMicros,
		period.ReservedUsdMicros,
		period.FinalizedUsdMicros,
		period.CreatedAt,
		period.UpdatedAt,
	})
}

func (*subscriberAllowancePeriodRows) Close()                                       {}
func (*subscriberAllowancePeriodRows) Err() error                                   { return nil }
func (*subscriberAllowancePeriodRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*subscriberAllowancePeriodRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (*subscriberAllowancePeriodRows) Values() ([]any, error)                       { return nil, nil }
func (*subscriberAllowancePeriodRows) RawValues() [][]byte                          { return nil }
func (*subscriberAllowancePeriodRows) Conn() *pgx.Conn                              { return nil }

func assignScanTargets(dest []any, values []any) error {
	if len(dest) != len(values) {
		return errors.New("unexpected scan target count")
	}
	for i := range dest {
		switch target := dest[i].(type) {
		case *string:
			*target = values[i].(string)
		case **string:
			*target = values[i].(*string)
		case *int64:
			*target = values[i].(int64)
		case **int64:
			*target = values[i].(*int64)
		case *uuid.UUID:
			*target = values[i].(uuid.UUID)
		case *pgtype.Timestamptz:
			*target = values[i].(pgtype.Timestamptz)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

var (
	allowanceSubscriberID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	allowanceAPIKeyID     = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	allowanceReservedAt   = time.Date(2026, 3, 1, 13, 30, 0, 0, time.UTC)
)

func testReservation() entitlement.Reservation {
	sixHour := entitlement.SixHourWindowAt(allowanceReservedAt)
	return entitlement.Reservation{
		ActionID:           "action-1",
		RouterRequestID:    "request-1",
		SubscriberID:       entitlement.SubscriberID(allowanceSubscriberID.String()),
		EntitlementVersion: 7,
		Plan:               entitlement.PlanMax,
		BillingPeriod: entitlement.Period{
			Kind:  entitlement.PeriodKindBilling,
			Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		},
		SixHourPeriod:         sixHour,
		APIKeyID:              allowanceAPIKeyID.String(),
		ClientSessionID:       "session-1",
		RequestedModel:        "claude-sonnet-4",
		ReservedUsdMicros:     25_000,
		CapacitySource:        entitlement.CapacitySourceIncludedRouter,
		ReservedAt:            allowanceReservedAt,
		BillingLimitUsdMicros: 50_000_000,
		SixHourLimitUsdMicros: 12_500_000,
	}
}

func reservedActionRow(reservation entitlement.Reservation) sqlc.RouterSubscriberAllowanceAction {
	clientSessionID := reservation.ClientSessionID
	return sqlc.RouterSubscriberAllowanceAction{
		ActionID:           reservation.ActionID,
		RouterRequestID:    reservation.RouterRequestID,
		SubscriberID:       allowanceSubscriberID,
		EntitlementVersion: reservation.EntitlementVersion,
		Plan:               string(reservation.Plan),
		BillingPeriodStart: utcTimestamptz(reservation.BillingPeriod.Start),
		BillingPeriodEnd:   utcTimestamptz(reservation.BillingPeriod.End),
		SixHourPeriodStart: utcTimestamptz(reservation.SixHourPeriod.Start),
		SixHourPeriodEnd:   utcTimestamptz(reservation.SixHourPeriod.End),
		APIKeyID:           allowanceAPIKeyID,
		ClientSessionID:    &clientSessionID,
		RequestedModel:     reservation.RequestedModel,
		ReservedUsdMicros:  reservation.ReservedUsdMicros,
		CapacitySource:     string(reservation.CapacitySource),
		State:              string(entitlement.ActionStateReserved),
		ReservedAt:         utcTimestamptz(reservation.ReservedAt),
	}
}

func TestReserveStoresHoldWithWindowLimits(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{value: reservedActionRow(reservation)}}}

	action, err := NewSubscriberAllowanceRepo(db).Reserve(context.Background(), reservation)

	require.NoError(t, err)
	assert.Equal(t, entitlement.ActionStateReserved, action.State)
	assert.Equal(t, reservation.ReservedUsdMicros, action.ReservedUsdMicros)
	assert.Equal(t, reservation.SixHourPeriod, action.SixHourPeriod)
	assert.Contains(t, db.args[0], reservation.BillingLimitUsdMicros)
	assert.Contains(t, db.args[0], reservation.SixHourLimitUsdMicros)
}

func accruedPeriodRow(reservation entitlement.Reservation, kind entitlement.PeriodKind) subscriberAllowancePeriodRow {
	period, limit := reservation.BillingPeriod, reservation.BillingLimitUsdMicros
	if kind == entitlement.PeriodKindSixHour {
		period, limit = reservation.SixHourPeriod, reservation.SixHourLimitUsdMicros
	}
	return subscriberAllowancePeriodRow{value: sqlc.RouterSubscriberAllowancePeriod{
		SubscriberID:       allowanceSubscriberID,
		PeriodKind:         string(kind),
		PeriodStart:        utcTimestamptz(period.Start),
		PeriodEnd:          utcTimestamptz(period.End),
		EntitlementVersion: reservation.EntitlementVersion,
		Plan:               string(reservation.Plan),
		LimitUsdMicros:     limit,
		ReservedUsdMicros:  reservation.ReservedUsdMicros,
	}}
}

func TestReserveWithinLimitsHoldsBothWindows(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{singleRows: []pgx.Row{
		subscriberAllowanceActionRow{value: reservedActionRow(reservation)},
		accruedPeriodRow(reservation, entitlement.PeriodKindBilling),
		accruedPeriodRow(reservation, entitlement.PeriodKindSixHour),
	}}

	action, err := NewSubscriberAllowanceRepo(db).ReserveWithinLimits(context.Background(), reservation)

	require.NoError(t, err)
	assert.Equal(t, entitlement.ActionStateReserved, action.State)
	assert.Equal(t, reservation.ReservedUsdMicros, action.ReservedUsdMicros)
	assert.Equal(t, 1, db.committed)
	assert.Contains(t, db.args[1], reservation.BillingLimitUsdMicros)
	assert.Contains(t, db.args[2], reservation.SixHourLimitUsdMicros)
}

func TestReserveWithinLimitsRefusesSpentWindowWithoutHolding(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{singleRows: []pgx.Row{
		subscriberAllowanceActionRow{value: reservedActionRow(reservation)},
		accruedPeriodRow(reservation, entitlement.PeriodKindBilling),
		subscriberAllowancePeriodRow{err: pgx.ErrNoRows},
	}}

	_, err := NewSubscriberAllowanceRepo(db).ReserveWithinLimits(context.Background(), reservation)

	require.ErrorIs(t, err, entitlement.ErrAllowanceExhausted)
	var exhausted entitlement.ExhaustedError
	require.ErrorAs(t, err, &exhausted)
	assert.Equal(t, entitlement.PeriodKindSixHour, exhausted.Period)
	// The refused turn is never dispatched, so neither the action nor the
	// month's accrual may survive it.
	assert.Zero(t, db.committed)
	assert.NotZero(t, db.rolledBack)
}

func TestReserveWithinLimitsReturnsStoredHoldForRedeliveredAction(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{singleRows: []pgx.Row{
		subscriberAllowanceActionRow{err: pgx.ErrNoRows},
		subscriberAllowanceActionRow{value: reservedActionRow(reservation)},
	}}

	action, err := NewSubscriberAllowanceRepo(db).ReserveWithinLimits(context.Background(), reservation)

	require.NoError(t, err)
	assert.Equal(t, reservation.ReservedUsdMicros, action.ReservedUsdMicros)
	assert.Equal(t, 1, db.committed)
}

func TestReserveWithinLimitsSkipsWindowsForLinkedCapacity(t *testing.T) {
	reservation := testReservation()
	reservation.CapacitySource = entitlement.CapacitySourceLinkedClaude
	stored := reservedActionRow(reservation)
	stored.CapacitySource = string(entitlement.CapacitySourceLinkedClaude)
	db := &subscriberAllowanceDB{singleRows: []pgx.Row{subscriberAllowanceActionRow{value: stored}}}

	action, err := NewSubscriberAllowanceRepo(db).ReserveWithinLimits(context.Background(), reservation)

	require.NoError(t, err)
	assert.Equal(t, entitlement.CapacitySourceLinkedClaude, action.CapacitySource)
	assert.Len(t, db.args, 1)
}

func TestReserveIsIdempotentForRedeliveredAction(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{value: reservedActionRow(reservation)}}}

	redelivered := reservation
	redelivered.ReservedAt = reservation.ReservedAt.Add(time.Second)
	action, err := NewSubscriberAllowanceRepo(db).Reserve(context.Background(), redelivered)

	require.NoError(t, err)
	assert.Equal(t, reservation.ReservedUsdMicros, action.ReservedUsdMicros)
	assert.Equal(t, entitlement.ActionStateReserved, action.State)
}

func TestReserveIsIdempotentForRedeliveryAfterWindowBoundary(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{value: reservedActionRow(reservation)}}}

	redelivered := reservation
	redelivered.ReservedAt = reservation.SixHourPeriod.End.Add(time.Minute)
	redelivered.SixHourPeriod = entitlement.SixHourWindowAt(redelivered.ReservedAt)
	action, err := NewSubscriberAllowanceRepo(db).Reserve(context.Background(), redelivered)

	require.NoError(t, err)
	assert.Equal(t, reservation.SixHourPeriod, action.SixHourPeriod)
	assert.Equal(t, entitlement.ActionStateReserved, action.State)
}

func TestReserveRejectsReusedActionIDForDifferentRequest(t *testing.T) {
	reservation := testReservation()
	stored := reservedActionRow(reservation)
	stored.RouterRequestID = "request-2"
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{value: stored}}}

	_, err := NewSubscriberAllowanceRepo(db).Reserve(context.Background(), reservation)

	assert.ErrorIs(t, err, entitlement.ErrAllowanceActionConflict)
}

func TestReserveReadsActionCommittedByConcurrentDelivery(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{
		{err: pgx.ErrNoRows},
		{value: reservedActionRow(reservation)},
	}}

	action, err := NewSubscriberAllowanceRepo(db).Reserve(context.Background(), reservation)

	require.NoError(t, err)
	assert.Equal(t, entitlement.ActionStateReserved, action.State)
	assert.Equal(t, reservation.ActionID, action.ActionID)
}

func TestReserveRejectsConflictingReplayOfSameAction(t *testing.T) {
	reservation := testReservation()
	stored := reservedActionRow(reservation)
	stored.ReservedUsdMicros = reservation.ReservedUsdMicros * 2
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{value: stored}}}

	_, err := NewSubscriberAllowanceRepo(db).Reserve(context.Background(), reservation)

	assert.ErrorIs(t, err, entitlement.ErrAllowanceActionConflict)
}

func TestReserveRejectsInvalidReservation(t *testing.T) {
	reservation := testReservation()
	reservation.SubscriberID = "not-a-uuid"
	db := &subscriberAllowanceDB{}

	_, err := NewSubscriberAllowanceRepo(db).Reserve(context.Background(), reservation)

	assert.ErrorIs(t, err, entitlement.ErrInvalidContract)
}

func TestFinalizeSettlesReservedAction(t *testing.T) {
	reservation := testReservation()
	finalizedAt := allowanceReservedAt.Add(time.Minute)
	retail := int64(18_000)
	servedModel := "claude-sonnet-4"
	stored := reservedActionRow(reservation)
	stored.State = string(entitlement.ActionStateFinalized)
	stored.ServedModel = &servedModel
	stored.RetailUsdMicros = &retail
	stored.FinalizedAt = utcTimestamptz(finalizedAt)
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{value: stored}}}

	action, err := NewSubscriberAllowanceRepo(db).Finalize(context.Background(), entitlement.Finalization{
		ActionID:        reservation.ActionID,
		ServedModel:     servedModel,
		RetailUsdMicros: retail,
		CapacitySource:  reservation.CapacitySource,
		FinalizedAt:     finalizedAt,
	})

	require.NoError(t, err)
	assert.Equal(t, entitlement.ActionStateFinalized, action.State)
	assert.Equal(t, retail, action.RetailUsdMicros)
	require.NotNil(t, action.FinalizedAt)
	assert.Equal(t, finalizedAt, *action.FinalizedAt)
}

func TestFinalizeRejectsAlreadyReleasedAction(t *testing.T) {
	reservation := testReservation()
	releasedAt := allowanceReservedAt.Add(time.Minute)
	stored := reservedActionRow(reservation)
	stored.State = string(entitlement.ActionStateReleased)
	stored.ReleasedAt = utcTimestamptz(releasedAt)
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{value: stored}}}

	_, err := NewSubscriberAllowanceRepo(db).Finalize(context.Background(), entitlement.Finalization{
		ActionID:        reservation.ActionID,
		ServedModel:     "claude-sonnet-4",
		RetailUsdMicros: 18_000,
		CapacitySource:  reservation.CapacitySource,
		FinalizedAt:     releasedAt,
	})

	assert.ErrorIs(t, err, entitlement.ErrAllowanceActionConflict)
}

func TestFinalizeAcceptsSettlementCommittedByConcurrentDelivery(t *testing.T) {
	reservation := testReservation()
	finalizedAt := allowanceReservedAt.Add(time.Minute)
	retail := int64(18_000)
	servedModel := "claude-sonnet-4"
	settled := reservedActionRow(reservation)
	settled.State = string(entitlement.ActionStateFinalized)
	settled.ServedModel = &servedModel
	settled.RetailUsdMicros = &retail
	settled.FinalizedAt = utcTimestamptz(finalizedAt)
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{
		{value: reservedActionRow(reservation)},
		{value: settled},
	}}

	action, err := NewSubscriberAllowanceRepo(db).Finalize(context.Background(), entitlement.Finalization{
		ActionID:        reservation.ActionID,
		ServedModel:     servedModel,
		RetailUsdMicros: retail,
		CapacitySource:  reservation.CapacitySource,
		FinalizedAt:     finalizedAt,
	})

	require.NoError(t, err)
	assert.Equal(t, entitlement.ActionStateFinalized, action.State)
}

func TestFinalizeReportsMissingAction(t *testing.T) {
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{err: pgx.ErrNoRows}}}

	_, err := NewSubscriberAllowanceRepo(db).Finalize(context.Background(), entitlement.Finalization{
		ActionID:        "missing",
		ServedModel:     "claude-sonnet-4",
		RetailUsdMicros: 1,
		CapacitySource:  entitlement.CapacitySourceIncludedRouter,
		FinalizedAt:     allowanceReservedAt,
	})

	assert.ErrorIs(t, err, entitlement.ErrAllowanceActionNotFound)
}

func TestReleaseReturnsHold(t *testing.T) {
	reservation := testReservation()
	releasedAt := allowanceReservedAt.Add(2 * time.Minute)
	stored := reservedActionRow(reservation)
	stored.State = string(entitlement.ActionStateReleased)
	stored.ReleasedAt = utcTimestamptz(releasedAt)
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{value: stored}}}

	action, err := NewSubscriberAllowanceRepo(db).Release(context.Background(), entitlement.Release{
		ActionID:   reservation.ActionID,
		ReleasedAt: releasedAt,
	})

	require.NoError(t, err)
	assert.Equal(t, entitlement.ActionStateReleased, action.State)
	require.NotNil(t, action.ReleasedAt)
	assert.Equal(t, releasedAt, *action.ReleasedAt)
}

func TestReleaseRejectsFinalizedAction(t *testing.T) {
	reservation := testReservation()
	finalizedAt := allowanceReservedAt.Add(time.Minute)
	retail := int64(18_000)
	servedModel := "claude-sonnet-4"
	stored := reservedActionRow(reservation)
	stored.State = string(entitlement.ActionStateFinalized)
	stored.ServedModel = &servedModel
	stored.RetailUsdMicros = &retail
	stored.FinalizedAt = utcTimestamptz(finalizedAt)
	db := &subscriberAllowanceDB{actionRows: []subscriberAllowanceActionRow{{value: stored}}}

	_, err := NewSubscriberAllowanceRepo(db).Release(context.Background(), entitlement.Release{
		ActionID:   reservation.ActionID,
		ReleasedAt: finalizedAt,
	})

	assert.ErrorIs(t, err, entitlement.ErrAllowanceActionConflict)
}

func TestUsageReportsBothWindows(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{periodRows: []sqlc.RouterSubscriberAllowancePeriod{
		{
			SubscriberID:       allowanceSubscriberID,
			PeriodKind:         string(entitlement.PeriodKindBilling),
			PeriodStart:        utcTimestamptz(reservation.BillingPeriod.Start),
			PeriodEnd:          utcTimestamptz(reservation.BillingPeriod.End),
			EntitlementVersion: reservation.EntitlementVersion,
			Plan:               string(reservation.Plan),
			LimitUsdMicros:     50_000_000,
			ReservedUsdMicros:  25_000,
			FinalizedUsdMicros: 1_000_000,
		},
		{
			SubscriberID:       allowanceSubscriberID,
			PeriodKind:         string(entitlement.PeriodKindSixHour),
			PeriodStart:        utcTimestamptz(reservation.SixHourPeriod.Start),
			PeriodEnd:          utcTimestamptz(reservation.SixHourPeriod.End),
			EntitlementVersion: reservation.EntitlementVersion,
			Plan:               string(reservation.Plan),
			LimitUsdMicros:     12_500_000,
			ReservedUsdMicros:  25_000,
			FinalizedUsdMicros: 500_000,
		},
	}}

	usage, err := NewSubscriberAllowanceRepo(db).Usage(context.Background(), reservation.SubscriberID, reservation.BillingPeriod, reservation.SixHourPeriod)

	require.NoError(t, err)
	assert.Equal(t, int64(1_025_000), usage.Billing.ConsumedUsdMicros())
	assert.Equal(t, int64(525_000), usage.SixHour.ConsumedUsdMicros())
	assert.Equal(t, int64(12_500_000), usage.SixHour.LimitUsdMicros)
}

func TestUsageReportsZeroForWindowsWithoutActivity(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{}

	usage, err := NewSubscriberAllowanceRepo(db).Usage(context.Background(), reservation.SubscriberID, reservation.BillingPeriod, reservation.SixHourPeriod)

	require.NoError(t, err)
	assert.Zero(t, usage.Billing.ConsumedUsdMicros())
	assert.Zero(t, usage.SixHour.ConsumedUsdMicros())
	assert.Equal(t, reservation.SixHourPeriod, usage.SixHour.Period)
}

func TestUsageFailsClosedOnReadError(t *testing.T) {
	reservation := testReservation()
	db := &subscriberAllowanceDB{queryErr: errors.New("connection reset")}

	_, err := NewSubscriberAllowanceRepo(db).Usage(context.Background(), reservation.SubscriberID, reservation.BillingPeriod, reservation.SixHourPeriod)

	assert.Error(t, err)
}
