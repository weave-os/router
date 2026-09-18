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

// SubscriberEntitlementRepo persists projected subscriber entitlements.
type SubscriberEntitlementRepo struct {
	queries *sqlc.Queries
}

// NewSubscriberEntitlementRepo binds entitlement projections to a SQLC database handle.
func NewSubscriberEntitlementRepo(db sqlc.DBTX) *SubscriberEntitlementRepo {
	return &SubscriberEntitlementRepo{queries: sqlc.New(db)}
}

// Project inserts a projection, advances its version, or accepts an identical retry.
func (r *SubscriberEntitlementRepo) Project(ctx context.Context, projected entitlement.Entitlement) error {
	if err := projected.Validate(); err != nil {
		return err
	}
	subscriberID, err := uuid.Parse(string(projected.SubscriberID))
	if err != nil {
		return entitlement.ErrInvalidContract
	}
	_, err = r.queries.UpsertSubscriberEntitlement(ctx, sqlc.UpsertSubscriberEntitlementParams{
		SubscriberID:                     subscriberID,
		Version:                          projected.Version,
		Plan:                             string(projected.Plan),
		Status:                           string(projected.Status),
		BillingPeriodStart:               pgtype.Timestamptz{Time: projected.BillingPeriod.Start, Valid: true},
		BillingPeriodEnd:                 pgtype.Timestamptz{Time: projected.BillingPeriod.End, Valid: true},
		EffectiveAt:                      pgtype.Timestamptz{Time: projected.EffectiveAt, Valid: true},
		MonthlyAllowanceUsdMicros:        projected.MonthlyAllowanceUsdMicros,
		NominalMonthlyAllowanceUsdMicros: projected.NominalMonthlyAllowanceUsdMicros,
		SixHourAllowanceUsdMicros:        projected.SixHourAllowanceUsdMicros,
		AutoTopUpEnabled:                 projected.AutoTopUpEnabled,
		ProjectedAt:                      pgtype.Timestamptz{Time: projected.ProjectedAt, Valid: true},
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("project subscriber entitlement: %w", err)
	}
	current, getErr := r.queries.GetSubscriberEntitlement(ctx, subscriberID)
	if getErr != nil {
		return fmt.Errorf("read rejected subscriber entitlement projection: %w", getErr)
	}
	if current.Version > projected.Version {
		return entitlement.ErrStaleProjection
	}
	if current.Version == projected.Version {
		return entitlement.ErrProjectionConflict
	}
	return fmt.Errorf("project subscriber entitlement: %w", err)
}

// Get returns the current entitlement projection for a subscriber.
func (r *SubscriberEntitlementRepo) Get(ctx context.Context, subscriberID entitlement.SubscriberID) (entitlement.Entitlement, error) {
	if !subscriberID.Valid() {
		return entitlement.Entitlement{}, entitlement.ErrInvalidContract
	}
	id, err := uuid.Parse(string(subscriberID))
	if err != nil {
		return entitlement.Entitlement{}, entitlement.ErrInvalidContract
	}
	row, err := r.queries.GetSubscriberEntitlement(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return entitlement.Entitlement{}, entitlement.ErrEntitlementNotFound
	}
	if err != nil {
		return entitlement.Entitlement{}, fmt.Errorf("get subscriber entitlement: %w", err)
	}
	return toSubscriberEntitlement(row)
}

func toSubscriberEntitlement(row sqlc.RouterSubscriberEntitlement) (entitlement.Entitlement, error) {
	projected := entitlement.Entitlement{
		SubscriberID: entitlement.SubscriberID(row.SubscriberID.String()),
		Version:      row.Version,
		Plan:         entitlement.Plan(row.Plan),
		Status:       entitlement.Status(row.Status),
		BillingPeriod: entitlement.Period{
			Kind:  entitlement.PeriodKindBilling,
			Start: subscriberTimestamptzUTCOrZero(row.BillingPeriodStart),
			End:   subscriberTimestamptzUTCOrZero(row.BillingPeriodEnd),
		},
		EffectiveAt:                      subscriberTimestamptzUTCOrZero(row.EffectiveAt),
		MonthlyAllowanceUsdMicros:        row.MonthlyAllowanceUsdMicros,
		NominalMonthlyAllowanceUsdMicros: row.NominalMonthlyAllowanceUsdMicros,
		SixHourAllowanceUsdMicros:        row.SixHourAllowanceUsdMicros,
		AutoTopUpEnabled:                 row.AutoTopUpEnabled,
		ProjectedAt:                      subscriberTimestamptzUTCOrZero(row.ProjectedAt),
	}
	if err := projected.Validate(); err != nil {
		return entitlement.Entitlement{}, fmt.Errorf("decode subscriber entitlement: %w", err)
	}
	return projected, nil
}

func subscriberTimestamptzUTCOrZero(value pgtype.Timestamptz) time.Time {
	timestamp := timestamptzOrZero(value)
	if timestamp.IsZero() {
		return timestamp
	}
	return timestamp.UTC()
}

var _ entitlement.EntitlementRepository = (*SubscriberEntitlementRepo)(nil)
