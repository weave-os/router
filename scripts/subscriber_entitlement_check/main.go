// Command subscriber_entitlement_check verifies monotonic projections on loopback Postgres.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/postgres"
	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/subscriptions/entitlement"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Subscriber entitlement integration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("Subscriber entitlement integration passed")
}

func run() error {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	parsed, err := url.Parse(dsn)
	if err != nil || dsn == "" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		return errors.New("ROUTER_TEST_DATABASE_URL must name an ephemeral loopback Postgres fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	subject, err := sqlc.New(tx).InsertCredentialSubject(ctx)
	if err != nil {
		return err
	}
	repo := postgres.NewSubscriberEntitlementRepo(tx)
	first := fixture(subject.ID, 1, entitlement.PlanMax, 50_000_000)
	if err := repo.Project(ctx, first); err != nil {
		return fmt.Errorf("project first version: %w", err)
	}
	if err := repo.Project(ctx, first); err != nil {
		return fmt.Errorf("retry identical version: %w", err)
	}
	conflicting := first
	conflicting.MonthlyAllowanceUsdMicros--
	if err := repo.Project(ctx, conflicting); !errors.Is(err, entitlement.ErrProjectionConflict) {
		return fmt.Errorf("same-version conflict was not rejected: %v", err)
	}
	second := fixture(subject.ID, 2, entitlement.PlanBoost, 200_000_000)
	if err := repo.Project(ctx, second); err != nil {
		return fmt.Errorf("advance projection: %w", err)
	}
	if err := repo.Project(ctx, first); !errors.Is(err, entitlement.ErrStaleProjection) {
		return fmt.Errorf("stale projection was not rejected: %v", err)
	}
	stored, err := repo.Get(ctx, entitlement.SubscriberID(subject.ID.String()))
	if err != nil {
		return err
	}
	if !sameEntitlement(stored, second) {
		return errors.New("stored projection differs from latest projected entitlement")
	}
	_, err = repo.Get(ctx, entitlement.SubscriberID(uuid.NewString()))
	if !errors.Is(err, entitlement.ErrEntitlementNotFound) {
		return fmt.Errorf("missing projection returned %v", err)
	}
	missingSubject := fixture(uuid.New(), 1, entitlement.PlanMax, 50_000_000)
	err = repo.Project(ctx, missingSubject)
	var constraintError *pgconn.PgError
	if !errors.As(err, &constraintError) || constraintError.Code != "23503" {
		return fmt.Errorf("projection accepted a non-credential subject: %v", err)
	}
	return nil
}

func sameEntitlement(left, right entitlement.Entitlement) bool {
	return left.SubscriberID == right.SubscriberID &&
		left.Version == right.Version &&
		left.Plan == right.Plan &&
		left.Status == right.Status &&
		left.BillingPeriod.Kind == right.BillingPeriod.Kind &&
		left.BillingPeriod.Start.Equal(right.BillingPeriod.Start) &&
		left.BillingPeriod.End.Equal(right.BillingPeriod.End) &&
		left.EffectiveAt.Equal(right.EffectiveAt) &&
		left.MonthlyAllowanceUsdMicros == right.MonthlyAllowanceUsdMicros &&
		left.NominalMonthlyAllowanceUsdMicros == right.NominalMonthlyAllowanceUsdMicros &&
		left.SixHourAllowanceUsdMicros == right.SixHourAllowanceUsdMicros &&
		left.AutoTopUpEnabled == right.AutoTopUpEnabled &&
		left.ProjectedAt.Equal(right.ProjectedAt)
}

func fixture(subscriberID uuid.UUID, version int64, plan entitlement.Plan, allowance int64) entitlement.Entitlement {
	start := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	return entitlement.Entitlement{
		SubscriberID:                     entitlement.SubscriberID(subscriberID.String()),
		Version:                          version,
		Plan:                             plan,
		Status:                           entitlement.StatusActive,
		BillingPeriod:                    entitlement.Period{Kind: entitlement.PeriodKindBilling, Start: start, End: start.AddDate(0, 1, 0)},
		EffectiveAt:                      start,
		MonthlyAllowanceUsdMicros:        allowance,
		NominalMonthlyAllowanceUsdMicros: allowance,
		SixHourAllowanceUsdMicros:        allowance / 124,
		AutoTopUpEnabled:                 true,
		ProjectedAt:                      start.Add(time.Duration(version) * time.Minute),
	}
}
