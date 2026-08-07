package analytics

import (
	"context"
	"errors"
	"time"
)

const (
	// DefaultLimit is the page size when the caller doesn't ask for one.
	DefaultLimit = 1000
	// MaxLimit caps a page so materializing one stays bounded in memory.
	MaxLimit = 10000
	// Holdback keeps the export's tail behind now: telemetry rows are written
	// off the request path and can commit slightly out of keyset order, so a
	// page that reached "now" could step over a row still in flight.
	Holdback = 60 * time.Second
)

// ErrWindowRequired is returned when neither since nor cursor is given;
// omitting both would scan from the beginning of retention.
var ErrWindowRequired = errors.New("since or cursor is required")

// Service serves the routing-decision export.
type Service struct {
	repo Repository
	now  func() time.Time
}

// NewService constructs a Service. now is injected so tests can pin the
// holdback boundary.
func NewService(repo Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repo: repo, now: now}
}

// ExportParams is one page request as it arrives from the transport, before
// defaults and the holdback are applied.
type ExportParams struct {
	InstallationID string
	// Since is inclusive, matched against ingest time. Ignored when Cursor is set.
	Since time.Time
	// Until is exclusive; zero means "up to the holdback boundary".
	Until time.Time
	// Cursor supersedes Since: it is a position, not a time, and resuming from
	// a time would replay or skip rows that share a timestamp.
	Cursor string
	// Limit is clamped to [1, MaxLimit]; zero means DefaultLimit.
	Limit int
}

// ExportRoutingDecisions returns one page of routing decisions plus the cursor
// that resumes after it. Returns ErrWindowRequired or ErrInvalidCursor for
// caller errors.
func (s *Service) ExportRoutingDecisions(ctx context.Context, p ExportParams) (Page, error) {
	after, err := ParseCursor(p.Cursor)
	if err != nil {
		return Page{}, err
	}
	if after.IsZero() && p.Since.IsZero() {
		return Page{}, ErrWindowRequired
	}

	limit := p.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	to := s.now().Add(-Holdback)
	if !p.Until.IsZero() && p.Until.Before(to) {
		to = p.Until
	}
	from := p.Since
	if !after.IsZero() {
		// The keyset predicate does the real work; from stays as a range bound
		// so the query keeps using the (installation_id, created_at, id) index.
		from = after.RecordedAt
	}
	if !from.Before(to) {
		return Page{}, nil
	}

	// One extra row is the has-more probe: cheaper and more honest than a
	// COUNT, and it avoids reporting "more" on an exactly-full final page.
	rows, err := s.repo.GetRoutingDecisions(ctx, Query{
		InstallationID: p.InstallationID,
		From:           from,
		To:             to,
		After:          after,
		Limit:          limit + 1,
	})
	if err != nil {
		return Page{}, err
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := Page{Decisions: rows, HasMore: hasMore}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = Cursor{RecordedAt: last.RecordedAt, ID: last.ID}.String()
	}
	return page, nil
}
