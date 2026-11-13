package analytics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"weave-os/router/internal/analytics"

	"github.com/stretchr/testify/require"
)

type fakeRepo struct {
	rows  []analytics.Decision
	lastQ analytics.Query
	err   error
	calls int
}

// GetRoutingDecisions replays the fake's rows honoring From/After/Limit so
// pagination tests exercise real keyset behavior rather than a canned page.
func (f *fakeRepo) GetRoutingDecisions(_ context.Context, q analytics.Query) ([]analytics.Decision, error) {
	f.lastQ = q
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]analytics.Decision, 0, q.Limit)
	for _, row := range f.rows {
		if row.RecordedAt.Before(q.From) || !row.RecordedAt.Before(q.To) {
			continue
		}
		if !q.After.IsZero() {
			if row.RecordedAt.Before(q.After.RecordedAt) ||
				(row.RecordedAt.Equal(q.After.RecordedAt) && row.ID <= q.After.ID) {
				continue
			}
		}
		out = append(out, row)
		if len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

func decisionsAt(base time.Time, n int) []analytics.Decision {
	out := make([]analytics.Decision, 0, n)
	for i := range n {
		out = append(out, analytics.Decision{
			ID:         string(rune('a' + i)),
			RecordedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestExportRequiresSinceOrCursor(t *testing.T) {
	svc := analytics.NewService(&fakeRepo{}, fixedNow(time.Now()))

	_, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{InstallationID: "inst"})

	require.ErrorIs(t, err, analytics.ErrWindowRequired)
}

func TestExportRejectsMalformedCursor(t *testing.T) {
	svc := analytics.NewService(&fakeRepo{}, fixedNow(time.Now()))

	_, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{
		InstallationID: "inst",
		Cursor:         "not-a-cursor",
	})

	require.ErrorIs(t, err, analytics.ErrInvalidCursor)
}

// Walking every page must yield each row exactly once: the property the whole
// keyset design exists for.
func TestExportPagesEveryRowExactlyOnce(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeRepo{rows: decisionsAt(base, 7)}
	svc := analytics.NewService(repo, fixedNow(base.Add(time.Hour)))

	params := analytics.ExportParams{InstallationID: "inst", Since: base, Limit: 3}
	var seen []string
	for range 10 {
		page, err := svc.ExportRoutingDecisions(context.Background(), params)
		require.NoError(t, err)
		for _, d := range page.Decisions {
			seen = append(seen, d.ID)
		}
		if !page.HasMore {
			break
		}
		params = analytics.ExportParams{InstallationID: "inst", Cursor: page.NextCursor, Limit: 3}
	}

	require.Equal(t, []string{"a", "b", "c", "d", "e", "f", "g"}, seen)
}

// A full final page must not claim more rows exist, or a consumer polls a
// phantom page forever.
func TestExportExactlyFullFinalPageReportsNoMore(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeRepo{rows: decisionsAt(base, 3)}
	svc := analytics.NewService(repo, fixedNow(base.Add(time.Hour)))

	page, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{
		InstallationID: "inst", Since: base, Limit: 3,
	})

	require.NoError(t, err)
	require.Len(t, page.Decisions, 3)
	require.False(t, page.HasMore)
}

// Rows inside the holdback are still being written; serving them risks paging
// past a row that commits a moment later.
func TestExportWithholdsRecentRows(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base.Add(90 * time.Second)
	repo := &fakeRepo{rows: decisionsAt(base, 120)}
	svc := analytics.NewService(repo, fixedNow(now))

	page, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{
		InstallationID: "inst", Since: base,
	})

	require.NoError(t, err)
	require.NotEmpty(t, page.Decisions)
	for _, d := range page.Decisions {
		require.True(t, d.RecordedAt.Before(now.Add(-analytics.Holdback)), "%s is inside the holdback", d.ID)
	}
}

// An explicit until narrower than the holdback boundary must win, or a
// backfill of a closed window would drag in newer rows.
func TestExportHonorsExplicitUntil(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeRepo{rows: decisionsAt(base, 10)}
	svc := analytics.NewService(repo, fixedNow(base.Add(time.Hour)))

	page, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{
		InstallationID: "inst", Since: base, Until: base.Add(4 * time.Second),
	})

	require.NoError(t, err)
	require.Len(t, page.Decisions, 4)
}

func TestExportClampsLimit(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeRepo{}
	svc := analytics.NewService(repo, fixedNow(base.Add(time.Hour)))

	_, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{
		InstallationID: "inst", Since: base, Limit: analytics.MaxLimit * 5,
	})

	require.NoError(t, err)
	require.Equal(t, analytics.MaxLimit+1, repo.lastQ.Limit)
}

func TestExportDefaultsLimit(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeRepo{}
	svc := analytics.NewService(repo, fixedNow(base.Add(time.Hour)))

	_, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{
		InstallationID: "inst", Since: base,
	})

	require.NoError(t, err)
	require.Equal(t, analytics.DefaultLimit+1, repo.lastQ.Limit)
}

// A window entirely inside the holdback has nothing safe to serve, and must
// not reach the database to find that out.
func TestExportEmptyWindowSkipsRepository(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeRepo{}
	svc := analytics.NewService(repo, fixedNow(now))

	page, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{
		InstallationID: "inst", Since: now.Add(-time.Second),
	})

	require.NoError(t, err)
	require.Empty(t, page.Decisions)
	require.False(t, page.HasMore)
	require.Zero(t, repo.calls)
}

func TestExportScopesToInstallation(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeRepo{}
	svc := analytics.NewService(repo, fixedNow(base.Add(time.Hour)))

	_, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{
		InstallationID: "inst-42", Since: base,
	})

	require.NoError(t, err)
	require.Equal(t, "inst-42", repo.lastQ.InstallationID)
}

func TestExportPropagatesRepositoryError(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	boom := errors.New("boom")
	svc := analytics.NewService(&fakeRepo{err: boom}, fixedNow(base.Add(time.Hour)))

	_, err := svc.ExportRoutingDecisions(context.Background(), analytics.ExportParams{
		InstallationID: "inst", Since: base,
	})

	require.ErrorIs(t, err, boom)
}
