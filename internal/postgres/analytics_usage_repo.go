package postgres

import (
	"context"
	"time"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

const usageQueryTimeout = 30 * time.Second

type UsageRepo struct {
	tx sqlc.DBTX
}

func NewUsageRepo(tx sqlc.DBTX) *UsageRepo {
	return &UsageRepo{tx: tx}
}

var _ analytics.UsageRepository = (*UsageRepo)(nil)

func (r *UsageRepo) GetUsageRows(ctx context.Context, q analytics.UsageQuery) ([]analytics.UsageRow, error) {
	params := sqlc.GetAnalyticsUsageRowsParams{
		GroupUser:           hasGroup(q, analytics.UsageGroupUser),
		GroupAPIKey:         hasGroup(q, analytics.UsageGroupAPIKey),
		GroupServedModel:    hasGroup(q, analytics.UsageGroupServedModel),
		GroupRequestedModel: hasGroup(q, analytics.UsageGroupRequestedModel),
		Emails:              q.Emails,
		APIKeyIds:           nil,
		RowLimit:            int32(q.Limit),
		RowOffset:           int32(q.Cursor.Offset),
		OrganizationID:      q.OrganizationID,
		FromTime:            pgtype.Timestamptz{Time: q.Start, Valid: true},
		ToTime:              pgtype.Timestamptz{Time: usageWindowEnd(q), Valid: true},
	}
	if len(q.APIKeyIDs) > 0 {
		params.APIKeyIds = make([]uuid.UUID, 0, len(q.APIKeyIDs))
		for _, id := range q.APIKeyIDs {
			parsed, err := uuid.Parse(id)
			if err != nil {
				return nil, err
			}
			params.APIKeyIds = append(params.APIKeyIds, parsed)
		}
	}

	rows, err := dbbudget.QueriesWithTimeout(r.tx, usageQueryTimeout).GetAnalyticsUsageRows(ctx, params)
	if err != nil {
		return nil, err
	}
	return mapRows(rows, usageRowFromSQL), nil
}

func hasGroup(q analytics.UsageQuery, g analytics.UsageGroup) bool {
	_, ok := q.Groups[g]
	return ok
}

func usageRowFromSQL(row sqlc.GetAnalyticsUsageRowsRow) analytics.UsageRow {
	return analytics.UsageRow{
		Date:                 row.UsageDate.Time.Format(time.DateOnly),
		RouterUserID:         stringPtrOrNil(row.RouterUserID),
		Email:                stringPtrOrNil(row.Email),
		DisplayName:          stringPtrOrNil(row.DisplayName),
		APIKeyID:             stringPtrOrNil(row.APIKeyID),
		APIKeyName:           stringPtrOrNil(row.APIKeyName),
		ServedModel:          stringPtrOrNil(row.ServedModel),
		ServedProvider:       stringPtrOrNil(row.ServedProvider),
		RequestedModel:       stringPtrOrNil(row.RequestedModel),
		Requests:             row.Requests,
		FailedRequests:       row.FailedRequests,
		SubscriptionRequests: row.SubscriptionRequests,
		InputTokens:          row.InputTokens,
		OutputTokens:         row.OutputTokens,
		CacheReadTokens:      row.CacheReadTokens,
		CacheWriteTokens:     row.CacheWriteTokens,
		CostUSDMicros:        row.CostUsdMicros,
		APIRateCostUSDMicros: row.APIRateCostUsdMicros,
	}
}

func usageWindowEnd(q analytics.UsageQuery) time.Time {
	dayAfterEnd := q.End.AddDate(0, 0, 1)
	if !q.Cursor.IngestedBefore.IsZero() && q.Cursor.IngestedBefore.Before(dayAfterEnd) {
		return q.Cursor.IngestedBefore
	}
	return dayAfterEnd
}
