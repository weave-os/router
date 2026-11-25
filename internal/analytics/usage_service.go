package analytics

import (
	"context"
	"errors"
)

var ErrUsageRepositoryMissing = errors.New("usage repository not configured")

type UsageService struct {
	repo UsageRepository
}

func NewUsageService(repo UsageRepository) *UsageService {
	return &UsageService{repo: repo}
}

func (s *UsageService) Usage(ctx context.Context, q UsageQuery) (UsagePage, error) {
	if s.repo == nil {
		return UsagePage{}, ErrUsageRepositoryMissing
	}
	limit := q.Limit
	if limit <= 0 {
		limit = UsageDefaultLimit
	}
	if limit > UsageMaxLimit {
		limit = UsageMaxLimit
	}
	q.Limit = limit + 1

	rows, err := s.repo.GetUsageRows(ctx, q)
	if err != nil {
		return UsagePage{}, err
	}
	page := UsagePage{Rows: rows}
	if len(rows) > limit {
		page.Rows = rows[:limit]
		page.NextCursor = UsageCursor{Offset: q.Cursor.Offset + limit}.String()
	}
	return page, nil
}
