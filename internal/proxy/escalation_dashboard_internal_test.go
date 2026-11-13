package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router/escalationdashboard"
)

type escalationDashboardStoreStub struct {
	filter     escalationdashboard.Filter
	snapshot   escalationdashboard.StoredSnapshot
	snapshotID string
	pageStart  int32
	pageLimit  int32
	readAt     time.Time
	err        error
}

func (s *escalationDashboardStoreStub) CreateSnapshot(_ context.Context, filter escalationdashboard.Filter) (escalationdashboard.StoredSnapshot, error) {
	s.filter = filter
	return s.snapshot, s.err
}

func (s *escalationDashboardStoreStub) SnapshotPage(_ context.Context, snapshotID string, pageStart, pageLimit int32, readAt time.Time) (escalationdashboard.StoredSnapshot, error) {
	s.snapshotID = snapshotID
	s.pageStart = pageStart
	s.pageLimit = pageLimit
	s.readAt = readAt
	return s.snapshot, s.err
}

func TestEscalationDashboardCapturesSnapshotTimeAndReturnsStoreResult(t *testing.T) {
	snapshotID := "236d9b20-02c0-4422-b0d9-a8b1b31049f8"
	store := &escalationDashboardStoreStub{snapshot: escalationdashboard.StoredSnapshot{
		SnapshotID: snapshotID,
		Snapshot: escalationdashboard.Snapshot{
			MatchingSessions: 3,
			Sessions:         []escalationdashboard.Session{{}, {}},
		},
	}}
	service := (&Service{}).WithEscalationDashboard(store)
	before := time.Now().UTC()

	snapshot, err := service.EscalationDashboard(context.Background(), escalationdashboard.Filter{
		Service: escalationdashboard.ServiceXGB,
		Limit:   2,
	})

	require.NoError(t, err)
	require.Equal(t, 3, snapshot.MatchingSessions)
	require.Equal(t, escalationdashboard.ServiceXGB, store.filter.Service)
	require.False(t, store.filter.CapturedAt.Before(before))
	require.False(t, store.filter.CapturedAt.After(time.Now().UTC()))
	require.Equal(t, escalationDashboardSnapshotTTL, store.filter.ExpiresAt.Sub(store.filter.CapturedAt))
	require.NotEmpty(t, snapshot.NextCursor)
	require.Empty(t, snapshot.PreviousCursor)
	require.True(t, snapshot.HasMore)
	decodedSnapshotID, pageStart, err := escalationdashboard.DecodeCursor(snapshot.NextCursor)
	require.NoError(t, err)
	require.Equal(t, snapshotID, decodedSnapshotID)
	require.Equal(t, int32(2), pageStart)
}

func TestEscalationDashboardReadsContinuationFromSameSnapshot(t *testing.T) {
	snapshotID := "236d9b20-02c0-4422-b0d9-a8b1b31049f8"
	cursor, err := escalationdashboard.EncodeCursor(snapshotID, 50)
	require.NoError(t, err)
	store := &escalationDashboardStoreStub{snapshot: escalationdashboard.StoredSnapshot{
		SnapshotID: snapshotID,
		Snapshot: escalationdashboard.Snapshot{
			CapturedAt:       time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC),
			MatchingSessions: 125,
			Sessions:         make([]escalationdashboard.Session, 50),
		},
	}}

	snapshot, err := (&Service{}).WithEscalationDashboard(store).EscalationDashboard(
		context.Background(), escalationdashboard.Filter{Limit: 50, Cursor: cursor},
	)

	require.NoError(t, err)
	require.Equal(t, snapshotID, store.snapshotID)
	require.Equal(t, int32(50), store.pageStart)
	require.Equal(t, int32(50), store.pageLimit)
	require.Equal(t, 50, snapshot.PageStart)
	require.NotEmpty(t, snapshot.PreviousCursor)
	require.NotEmpty(t, snapshot.NextCursor)
	_, previousStart, err := escalationdashboard.DecodeCursor(snapshot.PreviousCursor)
	require.NoError(t, err)
	require.Equal(t, int32(0), previousStart)
	_, nextStart, err := escalationdashboard.DecodeCursor(snapshot.NextCursor)
	require.NoError(t, err)
	require.Equal(t, int32(100), nextStart)
}

func TestEscalationDashboardRejectsInvalidPageAndPropagatesStoreErrors(t *testing.T) {
	service := (&Service{}).WithEscalationDashboard(&escalationDashboardStoreStub{})
	_, err := service.EscalationDashboard(context.Background(), escalationdashboard.Filter{Limit: 0})
	require.ErrorContains(t, err, "invalid escalation dashboard page")

	wantErr := errors.New("snapshot failed")
	service = (&Service{}).WithEscalationDashboard(&escalationDashboardStoreStub{err: wantErr})
	_, err = service.EscalationDashboard(context.Background(), escalationdashboard.Filter{Limit: 50})
	require.ErrorIs(t, err, wantErr)

	_, err = (&Service{}).EscalationDashboard(context.Background(), escalationdashboard.Filter{Limit: 50})
	require.ErrorIs(t, err, ErrEscalationJudgeUnavailable)
}
