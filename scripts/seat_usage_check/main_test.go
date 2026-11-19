// Run against a disposable database with router migrations applied:
//
//	ROUTER_TEST_DATABASE_URL=... go test ./scripts/seat_usage_check -count=1
package seat_usage_check_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/proxy/usage"
)

func TestSeatUsageKeepsTheNewestObservationPerUserAndProvider(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROUTER_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close(context.Background())) })
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	installation, err := postgres.NewRepository(tx, auth.NoOpEncryptor{}).Installations.Create(ctx, auth.CreateInstallationParams{
		ExternalID: uuid.NewString(), Name: "seat-usage-regression",
	})
	require.NoError(t, err)
	newUser := func() string {
		var id uuid.UUID
		require.NoError(t, tx.QueryRow(ctx, "INSERT INTO router.model_router_users (installation_id, email) VALUES ($1, $2) RETURNING id",
			installation.ID, uuid.NewString()+"@example.com").Scan(&id))
		return id.String()
	}
	userA, userB := newUser(), newUser()

	seats := postgres.NewSeatUsageRepo(tx)
	observedAt := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	resetAt := observedAt.Add(3 * time.Hour)
	snapshot := func(primary float64, at time.Time) usage.Snapshot {
		return usage.Snapshot{
			Primary:    usage.Window{UsedPercent: primary, WindowMinutes: 300, ResetAt: resetAt},
			Secondary:  usage.Window{UsedPercent: 18, WindowMinutes: 10080},
			ObservedAt: at,
		}
	}
	require.NoError(t, seats.RecordSeatUsage(ctx, userA, "claude", snapshot(42, observedAt)))
	require.NoError(t, seats.RecordSeatUsage(ctx, userA, "claude", snapshot(10, observedAt.Add(-time.Minute))))
	require.NoError(t, seats.RecordSeatUsage(ctx, userA, "codex", snapshot(7, observedAt)))
	require.NoError(t, seats.RecordSeatUsage(ctx, userB, "claude", snapshot(90, observedAt)))

	got, err := seats.ListSeatUsage(ctx, userA)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "claude", got[0].Provider)
	require.InDelta(t, 42.0, got[0].Snapshot.Primary.UsedPercent, 1e-9)
	require.True(t, resetAt.Equal(got[0].Snapshot.Primary.ResetAt))
	require.True(t, got[0].Snapshot.Secondary.ResetAt.IsZero())
	require.True(t, observedAt.Equal(got[0].Snapshot.ObservedAt))
	require.Equal(t, "codex", got[1].Provider)
	require.InDelta(t, 7.0, got[1].Snapshot.Primary.UsedPercent, 1e-9)

	require.NoError(t, seats.RecordSeatUsage(ctx, userA, "claude", snapshot(55, observedAt.Add(time.Minute))))
	got, err = seats.ListSeatUsage(ctx, userA)
	require.NoError(t, err)
	require.InDelta(t, 55.0, got[0].Snapshot.Primary.UsedPercent, 1e-9)

	primaryOnly := usage.Snapshot{
		Primary:    usage.Window{UsedPercent: 60, WindowMinutes: 300, ResetAt: resetAt},
		ObservedAt: observedAt.Add(2 * time.Minute),
	}
	secondaryOnly := usage.Snapshot{
		Secondary:  usage.Window{UsedPercent: 20, WindowMinutes: 10080},
		ObservedAt: observedAt.Add(3 * time.Minute),
	}
	require.NoError(t, seats.RecordSeatUsage(ctx, userA, "claude", primaryOnly))
	require.NoError(t, seats.RecordSeatUsage(ctx, userA, "claude", secondaryOnly))
	got, err = seats.ListSeatUsage(ctx, userA)
	require.NoError(t, err)
	require.InDelta(t, 60.0, got[0].Snapshot.Primary.UsedPercent, 1e-9, "an omitted window keeps its value until it resets")
	require.InDelta(t, 20.0, got[0].Snapshot.Secondary.UsedPercent, 1e-9)

	afterReset := usage.Snapshot{
		Secondary:  usage.Window{UsedPercent: 21, WindowMinutes: 10080},
		ObservedAt: resetAt.Add(time.Minute),
	}
	require.NoError(t, seats.RecordSeatUsage(ctx, userA, "claude", afterReset))
	got, err = seats.ListSeatUsage(ctx, userA)
	require.NoError(t, err)
	require.Zero(t, got[0].Snapshot.Primary.WindowMinutes, "an omitted window past its reset is dropped")
}
