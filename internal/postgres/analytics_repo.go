package postgres

import (
	"context"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// AnalyticsRepo implements analytics.Repository via SQLC.
type AnalyticsRepo struct {
	tx sqlc.DBTX
}

// NewAnalyticsRepo constructs an AnalyticsRepo backed by the given connection.
func NewAnalyticsRepo(tx sqlc.DBTX) *AnalyticsRepo {
	return &AnalyticsRepo{tx: tx}
}

var _ analytics.Repository = (*AnalyticsRepo)(nil)

// GetRoutingDecisions returns one keyset page of exported routing decisions.
func (r *AnalyticsRepo) GetRoutingDecisions(ctx context.Context, q analytics.Query) ([]analytics.Decision, error) {
	installationID, err := uuid.Parse(q.InstallationID)
	if err != nil {
		return nil, err
	}
	params := sqlc.GetRoutingDecisionsForExportParams{
		InstallationID: installationID,
		FromTime:       pgtype.Timestamptz{Time: q.From, Valid: true},
		ToTime:         pgtype.Timestamptz{Time: q.To, Valid: true},
		RowLimit:       int32(q.Limit),
	}
	if !q.After.IsZero() {
		params.CursorCreatedAt = pgtype.Timestamptz{Time: q.After.RecordedAt, Valid: true}
		params.CursorID = uuidOrNil(q.After.ID)
	}

	rows, err := sqlc.New(r.tx).GetRoutingDecisionsForExport(ctx, params)
	if err != nil {
		return nil, err
	}
	return mapRows(rows, decisionFromExportRow), nil
}

func decisionFromExportRow(row sqlc.GetRoutingDecisionsForExportRow) analytics.Decision {
	return analytics.Decision{
		ID:              row.ID.String(),
		RecordedAt:      row.CreatedAt.Time,
		RequestedAt:     row.Timestamp.Time,
		RequestID:       row.RequestID,
		TraceID:         row.TraceID,
		SessionID:       row.SessionID,
		DeviceID:        row.DeviceID,
		ClientApp:       row.ClientApp,
		TurnType:        row.TurnType,
		UserID:          uuidStringPtr(row.RouterUserID),
		UserEmail:       row.UserEmail,
		UserAccountUUID: uuidStringPtr(row.UserAccountUUID),

		RequestedModel:   row.RequestedModel,
		DecisionModel:    row.DecisionModel,
		DecisionProvider: row.DecisionProvider,
		CandidateModels:  row.CandidateModels,
		ChosenScore:      row.ChosenScore,
		DecisionReason:   row.DecisionReason,
		StickyHit:        row.StickyHit != nil && *row.StickyHit,
		FailoverUsed:     row.FailoverUsed != nil && *row.FailoverUsed,
		CrossFormat:      row.CrossFormat != nil && *row.CrossFormat,

		EstimatedInputTokens: int32PtrToInt64(row.EstimatedInputTokens),
		InputTokens:          int32PtrToInt64(row.InputTokens),
		OutputTokens:         int32PtrToInt64(row.OutputTokens),
		CacheCreationTokens:  int32PtrToInt64(row.CacheCreationTokens),
		CacheReadTokens:      int32PtrToInt64(row.CacheReadTokens),

		SubscriptionServed:  row.SubscriptionServed,
		ActualInputCostUSD:  servedCostUSD(row.ActualInputCostUsd, row.SubscriptionServed),
		ActualOutputCostUSD: servedCostUSD(row.ActualOutputCostUsd, row.SubscriptionServed),

		RouteLatencyMs:        row.RouteLatencyMs,
		UpstreamLatencyMs:     row.UpstreamLatencyMs,
		TotalLatencyMs:        row.TotalLatencyMs,
		TTFTMs:                row.TtftMs,
		UpstreamStatusCode:    int32PtrToInt64(row.UpstreamStatusCode),
		UpstreamFinishReason:  row.UpstreamFinishReason,
		StopReason:            row.StopReason,
		ToolUseBlocks:         int32PtrToInt64(row.ToolUseBlocks),
		InvalidToolArgsBlocks: int32PtrToInt64(row.InvalidToolArgsBlocks),
	}
}

func int32PtrToInt64(v *int32) *int64 {
	if v == nil {
		return nil
	}
	out := int64(*v)
	return &out
}

// servedCostUSD returns $0 for subscription-served turns (quota already paid) and the catalog rate otherwise.
func servedCostUSD(micros *int64, subscriptionServed bool) *float64 {
	if micros == nil {
		return nil
	}
	if subscriptionServed {
		zero := 0.0
		return &zero
	}
	return microsPtrToUSD(micros)
}

func microsPtrToUSD(micros *int64) *float64 {
	if micros == nil {
		return nil
	}
	out := microsToUSD(*micros)
	return &out
}

// uuidStringPtr returns nil for a NULL uuid so the export emits null rather
// than the all-zeroes uuid.
func uuidStringPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := uuid.UUID(u.Bytes).String()
	return &s
}
