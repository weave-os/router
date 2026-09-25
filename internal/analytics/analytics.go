// Package analytics is the read-only routing-decision export domain: row shape,
// keyset cursor, and field dictionary. Pure inner ring; Postgres access is
// behind Repository.
package analytics

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
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
	RolloutID       *string `json:"rollout_id"`
	DeviceID        *string `json:"device_id"`
	ClientApp       *string `json:"client_app"`
	TurnType        *string `json:"turn_type"`
	UserID          *string `json:"user_id"`
	UserEmail       *string `json:"user_email"`
	UserAccountUUID *string `json:"user_account_uuid"`

	RequestedModel       *string  `json:"requested_model"`
	DecisionModel        *string  `json:"decision_model"`
	DecisionProvider     *string  `json:"decision_provider"`
	RouteID              *string  `json:"route_id"`
	RoutingStrategy      *string  `json:"routing_strategy"`
	PolicyRouteKey       *string  `json:"policy_route_key"`
	ClusterRouterVersion *string  `json:"cluster_router_version"`
	CandidateModels      []string `json:"candidate_models"`
	ChosenScore          *float64 `json:"chosen_score"`
	// DecisionReason is free-form diagnostic prose whose format changes between
	// router versions; do not parse it — group on DecisionModel / StickyHit /
	// CandidateModels instead.
	DecisionReason                  *string                               `json:"decision_reason"`
	BlindExperimentArm              *auth.BlindExperimentArm              `json:"blind_experiment_arm"`
	BlindExperimentAssignmentSource *auth.BlindExperimentAssignmentSource `json:"blind_experiment_assignment_source"`
	BlindExperimentSubjectKey       *string                               `json:"blind_experiment_subject_key"`
	CohortExperimentID              *string                               `json:"cohort_experiment_id"`
	CohortGroupID                   *int64                                `json:"cohort_group_id"`
	CohortPhaseIndex                *int64                                `json:"cohort_phase_index"`
	CohortRevision                  *int64                                `json:"cohort_revision"`
	CohortScheduledArm              *auth.BlindExperimentArm              `json:"cohort_scheduled_arm"`
	CohortTreatmentApplied          *bool                                 `json:"cohort_treatment_applied"`
	CohortBypassReason              *auth.CohortBypassReason              `json:"cohort_bypass_reason"`
	// PolicyPin* are null when the turn carried no x-weave-policy-pin header.
	PolicyPinRequested *bool `json:"policy_pin_requested"`
	PolicyPinHonoured  *bool `json:"policy_pin_honoured"`
	StickyHit          bool  `json:"sticky_hit"`
	FailoverUsed       bool  `json:"failover_used"`
	CrossFormat        bool  `json:"cross_format"`

	EstimatedInputTokens *int64 `json:"estimated_input_tokens"`
	InputTokens          *int64 `json:"input_tokens"`
	OutputTokens         *int64 `json:"output_tokens"`
	CacheCreationTokens  *int64 `json:"cache_creation_tokens"`
	CacheReadTokens      *int64 `json:"cache_read_tokens"`

	// SubscriptionServed turns are covered by the caller's own quota; Actual* export as $0 while token counts stay real.
	SubscriptionServed bool `json:"subscription_served"`
	// Actual* price the served turn; counterfactual not exported — consumers reprice via RequestedModel + /v1/analytics/models.
	ActualInputCostUSD  *float64 `json:"actual_input_cost_usd"`
	ActualOutputCostUSD *float64 `json:"actual_output_cost_usd"`

	RouteLatencyMs           *int64  `json:"route_latency_ms"`
	UpstreamLatencyMs        *int64  `json:"upstream_latency_ms"`
	TotalLatencyMs           *int64  `json:"total_latency_ms"`
	TTFTMs                   *int64  `json:"ttft_ms"`
	UpstreamStatusCode       *int64  `json:"upstream_status_code"`
	UpstreamFinishReason     *string `json:"upstream_finish_reason"`
	StopReason               *string `json:"stop_reason"`
	ToolUseBlocks            *int64  `json:"tool_use_blocks"`
	InvalidToolArgsBlocks    *int64  `json:"invalid_tool_args_blocks"`
	SubscriberPlan           *string `json:"subscriber_plan"`
	EntitlementVersion       *int64  `json:"entitlement_version"`
	CapacitySource           *string `json:"capacity_source"`
	RetailUsageUSDMicros     *int64  `json:"retail_usage_usd_micros"`
	IncludedUsageUSDMicros   *int64  `json:"included_usage_usd_micros"`
	LinkedUsageUSDMicros     *int64  `json:"linked_usage_usd_micros"`
	PrepaidUsageUSDMicros    *int64  `json:"prepaid_usage_usd_micros"`
	SettlementFailed         *bool   `json:"settlement_failed"`
	ServingProfileID         *string `json:"serving_profile_id"`
	ServingProfileVersion    *string `json:"serving_profile_version"`
	ServingReleaseID         *string `json:"serving_release_id"`
	ServingBindingID         *string `json:"serving_binding_id"`
	BoostOptimizerVersion    *string `json:"boost_optimizer_version"`
	PolicyArtifactID         *string `json:"policy_artifact_id"`
	PolicyArtifactSHA256     *string `json:"policy_artifact_sha256"`
	RosterVersion            *string `json:"roster_version"`
	SelectionPolicyReleaseID *string `json:"selection_policy_release_id"`
	SelectionPolicySHA256    *string `json:"selection_policy_sha256"`

	// ClientGit* is the client-reported starting tree of a trial-mode session,
	// captured on its first turn only. Null everywhere else. HeadSHA is
	// abbreviated as the client printed it; compare by prefix.
	ClientGitHeadSHA *string `json:"client_git_head_sha"`
	ClientGitBranch  *string `json:"client_git_branch"`
	ClientGitDirty   *bool   `json:"client_git_dirty"`
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
