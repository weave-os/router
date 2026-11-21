package postgres_test

import (
	"context"
	"math"
	"testing"
	"time"

	"weave-os/router/internal/postgres"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	granularityFrom = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	granularityTo   = time.Date(2001, 1, 15, 0, 0, 0, 0, time.UTC)
)

func granularityAt(day, hour, minute int) time.Time {
	return time.Date(2001, 1, day, hour, minute, 0, 0, time.UTC)
}

type granularityTurn struct {
	at                        time.Time
	spanType, model           string
	input, output             int32
	requestedIn, requestedOut float64
	actualIn, actualOut       float64
}

func insertGranularityInstallation(ctx context.Context, t *testing.T, repo *postgres.TelemetryRepo, exec func(context.Context, string, ...any) error, turns []granularityTurn) uuid.UUID {
	t.Helper()
	id := uuid.New()
	require.NoError(t, exec(ctx, "INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, $2)", id, id.String()[:30]))
	t.Cleanup(func() {
		assert.NoError(t, exec(context.WithoutCancel(ctx), "DELETE FROM router.model_router_request_telemetry WHERE installation_id = $1", id))
		assert.NoError(t, exec(context.WithoutCancel(ctx), "DELETE FROM router.model_router_installations WHERE id = $1", id))
	})
	for _, turn := range turns {
		require.NoError(t, repo.InsertRequestTelemetry(ctx, turnrecord.InsertTelemetryParams{
			InstallationID:         id.String(),
			RequestID:              uuid.NewString(),
			SpanType:               turn.spanType,
			TraceID:                uuid.NewString(),
			Timestamp:              turn.at,
			RequestedModel:         turn.model,
			DecisionModel:          turn.model,
			DecisionProvider:       "anthropic",
			InputTokens:            turn.input,
			OutputTokens:           turn.output,
			RequestedInputCostUSD:  turn.requestedIn,
			RequestedOutputCostUSD: turn.requestedOut,
			ActualInputCostUSD:     turn.actualIn,
			ActualOutputCostUSD:    turn.actualOut,
		}))
	}
	return id
}

// Seeds one installation's turns (with a decision span and an out-of-window
// row that must be excluded) plus another installation's turn that only the
// cross-installation queries see. Gated on ROUTER_TEST_DATABASE_URL (see
// flag_definitions_test.go).
func seedGranularityTelemetry(t *testing.T) (*postgres.TelemetryRepo, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	cfg := testPool(t).Config()
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	exec := func(ctx context.Context, sql string, args ...any) error {
		_, err := pool.Exec(ctx, sql, args...)
		return err
	}
	repo := postgres.NewTelemetryRepo(pool)
	installation := insertGranularityInstallation(ctx, t, repo, exec, []granularityTurn{
		{at: granularityAt(2, 10, 15), spanType: "router.upstream", model: "model-a", input: 100, output: 20, requestedIn: 0.5, requestedOut: 0.25, actualIn: 0.2, actualOut: 0.1},
		{at: granularityAt(2, 10, 45), spanType: "router.upstream", model: "model-b", input: 50, output: 10, requestedIn: 0.1, requestedOut: 0.05, actualIn: 0.05, actualOut: 0.05},
		{at: granularityAt(4, 8, 0), spanType: "router.upstream", model: "model-a", input: 10, output: 5, requestedIn: 0.01, requestedOut: 0.02, actualIn: 0.01, actualOut: 0.01},
		{at: granularityAt(9, 12, 0), spanType: "router.upstream", model: "model-a", input: 1, output: 1, requestedIn: 1, requestedOut: 1, actualIn: 0.5, actualOut: 0.5},
		{at: granularityAt(2, 10, 20), spanType: "router.decision", model: "model-a", input: 9, output: 9, requestedIn: 9, requestedOut: 9, actualIn: 9, actualOut: 9},
		{at: granularityAt(20, 0, 0), spanType: "router.upstream", model: "model-a", input: 9, output: 9, requestedIn: 9, requestedOut: 9, actualIn: 9, actualOut: 9},
	})
	insertGranularityInstallation(ctx, t, repo, exec, []granularityTurn{
		{at: granularityAt(2, 11, 0), spanType: "router.upstream", model: "model-c", input: 7, output: 3, requestedIn: 0.3, requestedOut: 0.3, actualIn: 0.2, actualOut: 0.2},
	})
	return repo, installation
}

// granularityRequests maps each bucket width to the granularity strings that
// select it; anything other than "week" or "day" falls back to hourly.
func granularityRequests(granularity string) []string {
	if granularity == "hour" {
		return []string{"hour", "", "month"}
	}
	return []string{granularity}
}

func TestTelemetryTimeseriesGranularityAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	repo, installation := seedGranularityTelemetry(t)
	type costCase struct {
		granularity string
		scoped, all []turnrecord.TelemetryBucket
	}
	for _, tc := range []costCase{
		{
			granularity: "week",
			scoped:      []turnrecord.TelemetryBucket{{Bucket: granularityAt(1, 0, 0), RequestedCostUSD: 0.93, ActualCostUSD: 0.42}, {Bucket: granularityAt(8, 0, 0), RequestedCostUSD: 2, ActualCostUSD: 1}},
			all:         []turnrecord.TelemetryBucket{{Bucket: granularityAt(1, 0, 0), RequestedCostUSD: 1.53, ActualCostUSD: 0.82}, {Bucket: granularityAt(8, 0, 0), RequestedCostUSD: 2, ActualCostUSD: 1}},
		},
		{
			granularity: "day",
			scoped:      []turnrecord.TelemetryBucket{{Bucket: granularityAt(2, 0, 0), RequestedCostUSD: 0.9, ActualCostUSD: 0.4}, {Bucket: granularityAt(4, 0, 0), RequestedCostUSD: 0.03, ActualCostUSD: 0.02}, {Bucket: granularityAt(9, 0, 0), RequestedCostUSD: 2, ActualCostUSD: 1}},
			all:         []turnrecord.TelemetryBucket{{Bucket: granularityAt(2, 0, 0), RequestedCostUSD: 1.5, ActualCostUSD: 0.8}, {Bucket: granularityAt(4, 0, 0), RequestedCostUSD: 0.03, ActualCostUSD: 0.02}, {Bucket: granularityAt(9, 0, 0), RequestedCostUSD: 2, ActualCostUSD: 1}},
		},
		{
			granularity: "hour",
			scoped:      []turnrecord.TelemetryBucket{{Bucket: granularityAt(2, 10, 0), RequestedCostUSD: 0.9, ActualCostUSD: 0.4}, {Bucket: granularityAt(4, 8, 0), RequestedCostUSD: 0.03, ActualCostUSD: 0.02}, {Bucket: granularityAt(9, 12, 0), RequestedCostUSD: 2, ActualCostUSD: 1}},
			all:         []turnrecord.TelemetryBucket{{Bucket: granularityAt(2, 10, 0), RequestedCostUSD: 0.9, ActualCostUSD: 0.4}, {Bucket: granularityAt(2, 11, 0), RequestedCostUSD: 0.6, ActualCostUSD: 0.4}, {Bucket: granularityAt(4, 8, 0), RequestedCostUSD: 0.03, ActualCostUSD: 0.02}, {Bucket: granularityAt(9, 12, 0), RequestedCostUSD: 2, ActualCostUSD: 1}},
		},
	} {
		for _, granularity := range granularityRequests(tc.granularity) {
			t.Run(granularity, func(t *testing.T) {
				scoped, err := repo.GetTelemetryTimeseries(ctx, installation.String(), granularityFrom, granularityTo, granularity)
				require.NoError(t, err)
				assert.Equal(t, tc.scoped, utcCostBuckets(scoped))
				all, err := repo.GetTelemetryTimeseriesAll(ctx, granularityFrom, granularityTo, granularity)
				require.NoError(t, err)
				assert.Equal(t, tc.all, utcCostBuckets(all))
			})
		}
	}
	_, err := repo.GetTelemetryTimeseries(ctx, "not-a-uuid", granularityFrom, granularityTo, "day")
	require.Error(t, err, "a malformed installation ID is rejected")
}

func TestTelemetryModelBreakdownGranularityAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	repo, installation := seedGranularityTelemetry(t)
	modelA := func(at time.Time, count, tokens int64, cost float64) turnrecord.TelemetryModelBucket {
		return turnrecord.TelemetryModelBucket{Bucket: at, DecisionModel: "model-a", RequestCount: count, TotalTokens: tokens, ActualCostUSD: cost}
	}
	modelB := turnrecord.TelemetryModelBucket{DecisionModel: "model-b", RequestCount: 1, TotalTokens: 60, ActualCostUSD: 0.1}
	modelC := turnrecord.TelemetryModelBucket{DecisionModel: "model-c", RequestCount: 1, TotalTokens: 10, ActualCostUSD: 0.4}
	withBucket := func(bucket turnrecord.TelemetryModelBucket, when time.Time) turnrecord.TelemetryModelBucket {
		bucket.Bucket = when
		return bucket
	}
	type modelCase struct {
		granularity string
		scoped, all []turnrecord.TelemetryModelBucket
	}
	for _, tc := range []modelCase{
		{
			granularity: "week",
			scoped:      []turnrecord.TelemetryModelBucket{modelA(granularityAt(1, 0, 0), 2, 135, 0.32), withBucket(modelB, granularityAt(1, 0, 0)), modelA(granularityAt(8, 0, 0), 1, 2, 1)},
			all:         []turnrecord.TelemetryModelBucket{modelA(granularityAt(1, 0, 0), 2, 135, 0.32), withBucket(modelB, granularityAt(1, 0, 0)), withBucket(modelC, granularityAt(1, 0, 0)), modelA(granularityAt(8, 0, 0), 1, 2, 1)},
		},
		{
			granularity: "day",
			scoped:      []turnrecord.TelemetryModelBucket{modelA(granularityAt(2, 0, 0), 1, 120, 0.3), withBucket(modelB, granularityAt(2, 0, 0)), modelA(granularityAt(4, 0, 0), 1, 15, 0.02), modelA(granularityAt(9, 0, 0), 1, 2, 1)},
			all:         []turnrecord.TelemetryModelBucket{modelA(granularityAt(2, 0, 0), 1, 120, 0.3), withBucket(modelB, granularityAt(2, 0, 0)), withBucket(modelC, granularityAt(2, 0, 0)), modelA(granularityAt(4, 0, 0), 1, 15, 0.02), modelA(granularityAt(9, 0, 0), 1, 2, 1)},
		},
		{
			granularity: "hour",
			scoped:      []turnrecord.TelemetryModelBucket{modelA(granularityAt(2, 10, 0), 1, 120, 0.3), withBucket(modelB, granularityAt(2, 10, 0)), modelA(granularityAt(4, 8, 0), 1, 15, 0.02), modelA(granularityAt(9, 12, 0), 1, 2, 1)},
			all:         []turnrecord.TelemetryModelBucket{modelA(granularityAt(2, 10, 0), 1, 120, 0.3), withBucket(modelB, granularityAt(2, 10, 0)), withBucket(modelC, granularityAt(2, 11, 0)), modelA(granularityAt(4, 8, 0), 1, 15, 0.02), modelA(granularityAt(9, 12, 0), 1, 2, 1)},
		},
	} {
		for _, granularity := range granularityRequests(tc.granularity) {
			t.Run(granularity, func(t *testing.T) {
				scoped, err := repo.GetTelemetryModelBreakdown(ctx, installation.String(), granularityFrom, granularityTo, granularity)
				require.NoError(t, err)
				assert.Equal(t, tc.scoped, utcModelBuckets(scoped))
				all, err := repo.GetTelemetryModelBreakdownAll(ctx, granularityFrom, granularityTo, granularity)
				require.NoError(t, err)
				assert.Equal(t, tc.all, utcModelBuckets(all))
			})
		}
	}
	_, err := repo.GetTelemetryModelBreakdown(ctx, "not-a-uuid", granularityFrom, granularityTo, "day")
	require.Error(t, err, "a malformed installation ID is rejected")
}

// Costs are stored as integer micros, so comparing at micro precision drops
// only the float noise of the micros-to-USD conversion.
func roundToMicros(usd float64) float64 {
	return math.Round(usd*1e6) / 1e6
}

func utcCostBuckets(buckets []turnrecord.TelemetryBucket) []turnrecord.TelemetryBucket {
	for i := range buckets {
		buckets[i].Bucket = buckets[i].Bucket.UTC()
		buckets[i].RequestedCostUSD = roundToMicros(buckets[i].RequestedCostUSD)
		buckets[i].ActualCostUSD = roundToMicros(buckets[i].ActualCostUSD)
	}
	return buckets
}

func utcModelBuckets(buckets []turnrecord.TelemetryModelBucket) []turnrecord.TelemetryModelBucket {
	for i := range buckets {
		buckets[i].Bucket = buckets[i].Bucket.UTC()
		buckets[i].ActualCostUSD = roundToMicros(buckets[i].ActualCostUSD)
	}
	return buckets
}
