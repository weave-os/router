package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router/escalationdashboard"
	"weave-os/router/internal/sqlc"
)

func TestEscalationDashboardSnapshotPageRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	capturedAt := time.Now().UTC()
	expiresAt := capturedAt.Add(time.Minute)
	t.Cleanup(func() {
		err := sqlc.New(pool).DeleteExpiredEscalationDashboardSnapshots(ctx, pgtype.Timestamptz{
			Time:  expiresAt.Add(time.Second),
			Valid: true,
		})
		require.NoError(t, err)
	})
	repo := postgres.NewEscalationDashboardRepo(pool)

	created, err := repo.CreateSnapshot(ctx, escalationdashboard.Filter{
		Limit:      50,
		CapturedAt: capturedAt,
		ExpiresAt:  expiresAt,
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.SnapshotID)
	require.Equal(t, capturedAt.Unix(), created.CapturedAt.Unix())
	require.Empty(t, created.Sessions)

	page, err := repo.SnapshotPage(ctx, created.SnapshotID, 0, 50, capturedAt)
	require.NoError(t, err)
	require.Equal(t, created.SnapshotID, page.SnapshotID)
	require.Equal(t, created.CapturedAt, page.CapturedAt)

	_, err = repo.SnapshotPage(ctx, created.SnapshotID, 0, 50, expiresAt)
	require.ErrorIs(t, err, escalationdashboard.ErrExpiredCursor)
}
