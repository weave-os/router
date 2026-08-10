// Package analytics is the read-only routing-decision export domain: row shape,
// keyset cursor, and field dictionary. Pure inner ring; Postgres access is
// behind Repository.
package analytics

import (
	"context"
	"time"
)

// Decision is one exported routing decision: a single upstream action, not a
// user-visible request. Retries, failovers, compaction, and sub-agent turns
// each produce their own row, so consumers group on RequestID / TurnType.
type Decision struct {
	// ID is unique per row and stable across replays, so it is what a consumer
	// deduplicates on. RequestID is not: retries share one.
	ID string `json:"id"`
	// RecordedAt is ingest time and the export's ordering key.
	RecordedAt time.Time `json:"recorded_at"`
	// RequestedAt is event time, which can move backwards within a page.
	RequestedAt time.Time `json:"requested_at"`
	RequestID   string    `json:"request_id"`
	TraceID     string    `json:"trace_id"`

	SessionID       *string `json:"session_id"`
	DeviceID        *string `json:"device_id"`
	ClientApp       *string `json:"client_app"`
	TurnType        *string `json:"turn_type"`
	UserID          *string `json:"user_id"`
	UserEmail       *string `json:"user_email"`
	UserAccountUUID *string `json:"user_account_uuid"`

	RequestedModel   *string  `json:"requested_model"`
	DecisionModel    *string  `json:"decision_model"`
	DecisionProvider *string  `json:"decision_provider"`
	CandidateModels  []string `json:"candidate_models"`
	ChosenScore      *float64 `json:"chosen_score"`
	// DecisionReason is free-form diagnostic prose whose format changes between
	// router versions; do not parse it — group on DecisionModel / StickyHit /
	// CandidateModels instead.
	DecisionReason *string `json:"decision_reason"`
	StickyHit      bool    `json:"sticky_hit"`
	FailoverUsed   bool    `json:"failover_used"`
	CrossFormat    bool    `json:"cross_format"`

	EstimatedInputTokens *int64 `json:"estimated_input_tokens"`
	InputTokens          *int64 `json:"input_tokens"`
	OutputTokens         *int64 `json:"output_tokens"`
	CacheCreationTokens  *int64 `json:"cache_creation_tokens"`
	CacheReadTokens      *int64 `json:"cache_read_tokens"`

	// Actual* price the served turn; counterfactual not exported — consumers reprice via RequestedModel + /v1/analytics/models.
	ActualInputCostUSD  *float64 `json:"actual_input_cost_usd"`
	ActualOutputCostUSD *float64 `json:"actual_output_cost_usd"`

	RouteLatencyMs        *int64  `json:"route_latency_ms"`
	UpstreamLatencyMs     *int64  `json:"upstream_latency_ms"`
	TotalLatencyMs        *int64  `json:"total_latency_ms"`
	TTFTMs                *int64  `json:"ttft_ms"`
	UpstreamStatusCode    *int64  `json:"upstream_status_code"`
	UpstreamFinishReason  *string `json:"upstream_finish_reason"`
	StopReason            *string `json:"stop_reason"`
	ToolUseBlocks         *int64  `json:"tool_use_blocks"`
	InvalidToolArgsBlocks *int64  `json:"invalid_tool_args_blocks"`
}

// Query is one normalized page request: window [From, To), optional keyset
// cursor, and Limit rows to fetch.
type Query struct {
	InstallationID string
	From           time.Time
	To             time.Time
	// After is the last row of the previous page; zero value means first page.
	After Cursor
	Limit int
}

// Repository reads routing decisions for the export.
type Repository interface {
	// GetRoutingDecisions returns up to Limit rows ordered by the
	// (RecordedAt, ID) keyset, ascending.
	GetRoutingDecisions(ctx context.Context, q Query) ([]Decision, error)
}

// Page is one export page plus the cursor that resumes after it.
type Page struct {
	Decisions []Decision
	// NextCursor is returned even on the final page so a drained consumer can
	// persist it and resume once new rows land. Empty only when the page is empty.
	NextCursor string
	HasMore    bool
}
