package proxy

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panicTelemetryRepo is a turnrecord.TelemetryRepository whose InsertRequestTelemetry
// always panics, simulating a bug in the telemetry sink.
type panicTelemetryRepo struct{}

func (panicTelemetryRepo) InsertRequestTelemetry(ctx context.Context, p turnrecord.InsertTelemetryParams) error {
	panic("boom: telemetry insert")
}

func (panicTelemetryRepo) GetTelemetrySummary(ctx context.Context, installationID string, from, to time.Time) (turnrecord.TelemetrySummary, error) {
	return turnrecord.TelemetrySummary{}, nil
}

func (panicTelemetryRepo) GetTelemetryTimeseries(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryBucket, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetrySummaryAll(ctx context.Context, from, to time.Time) (turnrecord.TelemetrySummary, error) {
	return turnrecord.TelemetrySummary{}, nil
}

func (panicTelemetryRepo) GetTelemetryTimeseriesAll(ctx context.Context, from, to time.Time, granularity string) ([]turnrecord.TelemetryBucket, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryRows(ctx context.Context, installationID string, from, to time.Time, limit int32) ([]turnrecord.TelemetryRow, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryRowsAll(ctx context.Context, from, to time.Time, limit int32) ([]turnrecord.TelemetryRow, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryModelBreakdown(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryModelBucket, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryModelBreakdownAll(ctx context.Context, from, to time.Time, granularity string) ([]turnrecord.TelemetryModelBucket, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetSessionCost(ctx context.Context, installationID, sessionID string) (turnrecord.SessionCost, error) {
	return turnrecord.SessionCost{}, turnrecord.ErrSessionCostNotFound
}

func (panicTelemetryRepo) GetTelemetryBySessionSequence(ctx context.Context, installationID uuid.UUID, sessionKey []byte, role string, seq int) (turnrecord.TelemetryTurnResult, error) {
	return turnrecord.TelemetryTurnResult{}, nil
}

type panicLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *panicLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *panicLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestFireTelemetryRecoversFromPanic proves a panic inside the async
// telemetry insert is caught and logged instead of crashing the process.
func TestFireTelemetryRecoversFromPanic(t *testing.T) {
	// Prime observability's sync.Once before overriding slog.Default; otherwise the goroutine's
	// first Get() call races SetDefault and resets the handler.
	observability.Get()

	var buf panicLogBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	s := &Service{engine: dispatch.NewEngine(nil), hmmServing: hmmPorts(), telemetry: panicTelemetryRepo{}}

	assert.NotPanics(t, func() {
		s.fireTelemetry(t.Context(), turnrecord.InsertTelemetryParams{RequestID: "req-1"})
	})

	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "Background goroutine panicked")
	}, 2*time.Second, 10*time.Millisecond)

	assert.Contains(t, buf.String(), "Background goroutine panicked")
	assert.Contains(t, buf.String(), "fireTelemetry")
}

func (panicTelemetryRepo) GetTelemetryDashboardFacts(context.Context, string, time.Time, time.Time, string) ([]turnrecord.TelemetryDashboardFact, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryDashboardActiveUsers(context.Context, string, time.Time, time.Time, string) ([]turnrecord.TelemetryActiveUsers, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryDashboardPeople(context.Context, string) ([]turnrecord.TelemetryPerson, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetrySubscriptionPoolSeats(context.Context, string, time.Time, time.Time) ([]turnrecord.TelemetrySubscriptionSeat, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryDashboardDrillDown(context.Context, string, time.Time, time.Time) ([]turnrecord.TelemetryDrillDownRow, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryDashboardIndividualFacts(context.Context, string, time.Time, time.Time, string) ([]turnrecord.TelemetryIndividualFact, error) {
	return nil, nil
}
