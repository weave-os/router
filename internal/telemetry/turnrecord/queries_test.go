package turnrecord_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/telemetry/turnrecord"
)

type sessionCostRepo struct {
	turnrecord.TelemetryRepository
	cost turnrecord.SessionCost
}

func (r sessionCostRepo) GetSessionCost(context.Context, string, string) (turnrecord.SessionCost, error) {
	return r.cost, nil
}

func TestQueriesWithoutRepositoryReturnZeroValues(t *testing.T) {
	q := turnrecord.Queries{}
	ctx := context.Background()
	now := time.Now()

	summary, err := q.MetricsSummary(ctx, "inst", now, now)
	require.NoError(t, err)
	require.Equal(t, turnrecord.TelemetrySummary{}, summary)

	rows, err := q.MetricsRowsAll(ctx, now, now, 10)
	require.NoError(t, err)
	require.Nil(t, rows)

	_, err = q.SessionCost(ctx, "inst", "session")
	require.ErrorIs(t, err, turnrecord.ErrSessionCostUnavailable)
}

func TestQueriesSessionCostValidatesSessionID(t *testing.T) {
	q := turnrecord.Queries{Repo: sessionCostRepo{cost: turnrecord.SessionCost{RequestCount: 3}}}
	ctx := context.Background()

	_, err := q.SessionCost(ctx, "inst", "")
	require.ErrorIs(t, err, turnrecord.ErrInvalidSessionID)

	_, err = q.SessionCost(ctx, "inst", strings.Repeat("a", requestcontext.MaxClientIdentifierLen+1))
	require.ErrorIs(t, err, turnrecord.ErrInvalidSessionID)

	cost, err := q.SessionCost(ctx, "inst", strings.Repeat("a", requestcontext.MaxClientIdentifierLen))
	require.NoError(t, err)
	require.Equal(t, int64(3), cost.RequestCount)
}
