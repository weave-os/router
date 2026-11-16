package analytics

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
)

type Decision struct {
	ID          string    `json:"id"`
	RecordedAt  time.Time `json:"recorded_at"`
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

	RequestedModel                  *string                               `json:"requested_model"`
	DecisionModel                   *string                               `json:"decision_model"`
	DecisionProvider                *string                               `json:"decision_provider"`
	RouteID                         *string                               `json:"route_id"`
	RoutingStrategy                 *string                               `json:"routing_strategy"`
	PolicyRouteKey                  *string                               `json:"policy_route_key"`
	ClusterRouterVersion            *string                               `json:"cluster_router_version"`
	CandidateModels                 []string                              `json:"candidate_models"`
	ChosenScore                     *float64                              `json:"chosen_score"`
	DecisionReason                  *string                               `json:"decision_reason"`
	BlindExperimentArm              *auth.BlindExperimentArm              `json:"blind_experiment_arm"`
	BlindExperimentAssignmentSource *auth.BlindExperimentAssignmentSource `json:"blind_experiment_assignment_source"`
	BlindExperimentSubjectKey       *string                               `json:"blind_experiment_subject_key"`
	BlindExperimentMode             *auth.BlindExperimentMode             `json:"blind_experiment_mode"`
	BlindExperimentSessionKey       *string                               `json:"blind_experiment_session_key"`
	CohortExperimentID              *string                               `json:"cohort_experiment_id"`
	CohortGroupID                   *int64                                `json:"cohort_group_id"`
	CohortPhaseIndex                *int64                                `json:"cohort_phase_index"`
	CohortRevision                  *int64                                `json:"cohort_revision"`
	CohortScheduledArm              *auth.BlindExperimentArm              `json:"cohort_scheduled_arm"`
	CohortTreatmentApplied          *bool                                 `json:"cohort_treatment_applied"`
	CohortBypassReason              *auth.CohortBypassReason              `json:"cohort_bypass_reason"`
	BlindExperimentModelSource      *auth.BlindExperimentModelSource      `json:"blind_experiment_model_source"`
	BlindExperimentTreatmentApplied *bool                                 `json:"blind_experiment_treatment_applied"`
	BlindExperimentBaselineModel    *string                               `json:"blind_experiment_baseline_model"`
	BlindExperimentModelChanged     *bool                                 `json:"blind_experiment_model_changed"`
	PolicyPinRequested              *bool                                 `json:"policy_pin_requested"`
	PolicyPinHonoured               *bool                                 `json:"policy_pin_honoured"`
	StickyHit                       bool                                  `json:"sticky_hit"`
	FailoverUsed                    bool                                  `json:"failover_used"`
	CrossFormat                     bool                                  `json:"cross_format"`
	ErrorClass                      *string                               `json:"error_class"`
	FailureOwner                    *string                               `json:"failure_owner"`

	EstimatedInputTokens  *int64  `json:"estimated_input_tokens"`
	InputTokens           *int64  `json:"input_tokens"`
	OutputTokens          *int64  `json:"output_tokens"`
	CacheCreationTokens   *int64  `json:"cache_creation_tokens"`
	CacheReadTokens       *int64  `json:"cache_read_tokens"`
	CacheCreation1hTokens *int64  `json:"cache_creation_1h_tokens"`
	ReasoningTokens       *int64  `json:"reasoning_tokens"`
	Speed                 *string `json:"speed"`
	InferenceGeo          *string `json:"inference_geo"`

	SubscriptionServed    bool                   `json:"subscription_served"`
	CredentialSource      *string                `json:"credential_source"`
	SubscriptionAccountID *string                `json:"subscription_account_id"`
	SubscriptionOwnerID   *string                `json:"subscription_owner_id"`
	SubscriptionTier      *auth.SubscriptionTier `json:"subscription_tier"`
	IntendedModelFamily   *string                `json:"intended_model_family"`
	FinalModelFamily      *string                `json:"final_model_family"`
	ActualInputCostUSD    *float64               `json:"actual_input_cost_usd"`
	ActualOutputCostUSD   *float64               `json:"actual_output_cost_usd"`
	APIRateInputCostUSD   *float64               `json:"api_rate_input_cost_usd"`
	APIRateOutputCostUSD  *float64               `json:"api_rate_output_cost_usd"`

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
	ServingTarget            *string `json:"serving_target"`
	BoostOptimizerVersion    *string `json:"boost_optimizer_version"`
	PolicyArtifactID         *string `json:"policy_artifact_id"`
	PolicyArtifactSHA256     *string `json:"policy_artifact_sha256"`
	RosterVersion            *string `json:"roster_version"`
	SelectionPolicyReleaseID *string `json:"selection_policy_release_id"`
	SelectionPolicySHA256    *string `json:"selection_policy_sha256"`

	ClientGitHeadSHA *string `json:"client_git_head_sha"`
	ClientGitBranch  *string `json:"client_git_branch"`
	ClientGitDirty   *bool   `json:"client_git_dirty"`
}

type Query struct {
	InstallationID string
	From           time.Time
	To             time.Time
	After          Cursor
	Limit          int
}

type Repository interface {
	GetRoutingDecisions(ctx context.Context, q Query) ([]Decision, error)
}

type Page struct {
	Decisions  []Decision
	NextCursor string
	HasMore    bool
}
