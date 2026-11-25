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
		ToTime:              pgtype.Timestamptz{Time: q.End.AddDate(0, 0, 1), Valid: true},
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
		RouterUserID:         stringFromValue(row.RouterUserID),
		Email:                stringFromValue(row.Email),
		DisplayName:          stringFromValue(row.DisplayName),
		APIKeyID:             stringFromValue(row.APIKeyID),
		APIKeyName:           stringFromValue(row.APIKeyName),
		ServedModel:          stringFromValue(row.ServedModel),
		ServedProvider:       stringFromValue(row.ServedProvider),
		RequestedModel:       stringFromValue(row.RequestedModel),
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

func stringFromValue(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
