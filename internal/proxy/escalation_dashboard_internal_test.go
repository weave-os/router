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
	filter   escalationdashboard.Filter
	snapshot escalationdashboard.Snapshot
	err      error
}

func (s *escalationDashboardStoreStub) Snapshot(_ context.Context, filter escalationdashboard.Filter) (escalationdashboard.Snapshot, error) {
	s.filter = filter
	return s.snapshot, s.err
}

func TestEscalationDashboardCapturesSnapshotTimeAndReturnsStoreResult(t *testing.T) {
	store := &escalationDashboardStoreStub{snapshot: escalationdashboard.Snapshot{MatchingSessions: 3}}
	service := (&Service{}).WithEscalationDashboard(store)
	before := time.Now().UTC()

	snapshot, err := service.EscalationDashboard(context.Background(), escalationdashboard.Filter{
		Service: escalationdashboard.ServiceXGB,
		Limit:   50,
		Offset:  100,
	})

	require.NoError(t, err)
	require.Equal(t, 3, snapshot.MatchingSessions)
	require.Equal(t, escalationdashboard.ServiceXGB, store.filter.Service)
	require.Equal(t, int32(100), store.filter.Offset)
	require.False(t, store.filter.CapturedAt.Before(before))
	require.False(t, store.filter.CapturedAt.After(time.Now().UTC()))
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
