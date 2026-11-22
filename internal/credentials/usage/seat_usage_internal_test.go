package usage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordedSeatUsage struct {
	routerUserID string
	provider     string
	observedAt   time.Time
}

type channelSeatUsageRecorder chan recordedSeatUsage

func (c channelSeatUsageRecorder) RecordSeatUsage(_ context.Context, routerUserID, provider string, snapshot Snapshot) error {
	c <- recordedSeatUsage{routerUserID: routerUserID, provider: provider, observedAt: snapshot.ObservedAt}
	return nil
}

func TestRecordSeatPersistsAtMostOncePerIntervalPerUserAndProvider(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	now := start
	recorded := make(channelSeatUsageRecorder, 8)
	observer := NewObserver([]byte("synthetic-salt"), time.Minute, func() time.Time { return now }).WithSeatUsageRecorder(recorded)
	snapshot := Snapshot{Primary: Window{UsedPercent: 42, WindowMinutes: 300}}

	observer.RecordSeat(t.Context(), "user-a", "claude", snapshot)
	now = start.Add(30 * time.Second)
	observer.RecordSeat(t.Context(), "user-a", "claude", snapshot)
	observer.RecordSeat(t.Context(), "user-a", "codex", snapshot)
	observer.RecordSeat(t.Context(), "", "claude", snapshot)
	now = start.Add(seatUsageRecordInterval)
	observer.RecordSeat(t.Context(), "user-a", "claude", snapshot)

	var got []recordedSeatUsage
	for range 3 {
		select {
		case record := <-recorded:
			got = append(got, record)
		case <-time.After(5 * time.Second):
			t.Fatalf("expected 3 recorded observations, got %d", len(got))
		}
	}
	require.ElementsMatch(t, []recordedSeatUsage{
		{routerUserID: "user-a", provider: "claude", observedAt: start},
		{routerUserID: "user-a", provider: "codex", observedAt: start.Add(30 * time.Second)},
		{routerUserID: "user-a", provider: "claude", observedAt: start.Add(seatUsageRecordInterval)},
	}, got)
	select {
	case extra := <-recorded:
		t.Fatalf("unexpected extra observation %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRecordSeatWithoutRecorderIsANoOp(t *testing.T) {
	var unset *Observer
	unset.RecordSeat(t.Context(), "user-a", "claude", Snapshot{})
	NewObserver([]byte("synthetic-salt"), time.Minute, time.Now).RecordSeat(t.Context(), "user-a", "claude", Snapshot{})
}
