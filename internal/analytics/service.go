package analytics

import (
	"context"
	"errors"
	"time"
)

const (
	DefaultLimit = 1000
	MaxLimit     = 10000
	Holdback     = 60 * time.Second
)

var ErrWindowRequired = errors.New("since or cursor is required")

type Service struct {
	repo Repository
	now  func() time.Time
}

func NewService(repo Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repo: repo, now: now}
}

type ExportParams struct {
	InstallationID string
	Since          time.Time
	Until          time.Time
	Cursor         string
	Limit          int
}

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
		from = after.RecordedAt
	}
	if !from.Before(to) {
		return Page{}, nil
	}

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
