package analytics

import (
	"context"
	"errors"
	"time"
)

var ErrUsageRepositoryMissing = errors.New("usage repository not configured")

type UsageService struct {
	repo UsageRepository
	now  func() time.Time
}

func NewUsageService(repo UsageRepository, now func() time.Time) *UsageService {
	if now == nil {
		now = time.Now
	}
	return &UsageService{repo: repo, now: now}
}

func (s *UsageService) Usage(ctx context.Context, q UsageQuery) (UsagePage, error) {
	if s.repo == nil {
		return UsagePage{}, ErrUsageRepositoryMissing
	}
	if q.Cursor.IngestedBefore.IsZero() {
		q.Cursor.IngestedBefore = s.now().Add(-Holdback).UTC()
	}
	pageSize := q.Limit
	q.Limit = pageSize + 1

	rows, err := s.repo.GetUsageRows(ctx, q)
	if err != nil {
		return UsagePage{}, err
	}
	page := UsagePage{Rows: rows}
	if len(rows) > pageSize {
		page.Rows = rows[:pageSize]
		page.NextCursor = UsageCursor{Offset: q.Cursor.Offset + pageSize, IngestedBefore: q.Cursor.IngestedBefore}.String()
	}
	return page, nil
}
