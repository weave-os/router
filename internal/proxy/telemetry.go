package proxy

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// InstallationIDContextKey is the request-context key for the authenticated installation UUID.
type InstallationIDContextKey struct{}

// TelemetryRepository persists per-request telemetry rows used by the UI dashboard.
type TelemetryRepository interface {
	InsertRequestTelemetry(ctx context.Context, p InsertTelemetryParams) error
	GetTelemetrySummary(ctx context.Context, installationID string, from, to time.Time) (TelemetrySummary, error)
	GetTelemetryTimeseries(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryBucket, error)
	GetTelemetrySummaryAll(ctx context.Context, from, to time.Time) (TelemetrySummary, error)
	GetTelemetryTimeseriesAll(ctx context.Context, from, to time.Time, granularity string) ([]TelemetryBucket, error)
	GetTelemetryRows(ctx context.Context, installationID string, from, to time.Time, limit int32) ([]TelemetryRow, error)
	GetTelemetryRowsAll(ctx context.Context, from, to time.Time, limit int32) ([]TelemetryRow, error)
	GetTelemetryModelBreakdown(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryModelBucket, error)
	GetTelemetryModelBreakdownAll(ctx context.Context, from, to time.Time, granularity string) ([]TelemetryModelBucket, error)
	GetTelemetryBySessionSequence(ctx context.Context, installationID uuid.UUID, sessionKey []byte, role string, seq int) (TelemetryTurnResult, error)
	GetSessionCost(ctx context.Context, installationID, sessionID string) (SessionCost, error)
}

// Costs are USD micros ($1.00 = 1,000,000) summed as integers so no float rounding accumulates.
// Actual = router's chosen binding; Requested = client's originally-requested model.
type SessionCost struct {
	SessionID              string
	RequestCount           int64
	ActualCostUSDMicros    int64
	RequestedCostUSDMicros int64
	InputTokens            int64
	OutputTokens           int64
	CacheCreationTokens    int64
	CacheReadTokens        int64
	LastRecordedAt         time.Time
}

// InsertTelemetryParams mirrors one router.upstream span row.
type InsertTelemetryParams struct {
	InstallationID string
	// APIKeyID attributes the row to the authenticating api key (per-key spend
	// audit). Empty leaves the column NULL.
	APIKeyID             string
	RequestID            string
	SpanType             string
	TraceID              string
	Timestamp            time.Time
	RequestedModel       string
	DecisionModel        string
	DecisionProvider     string
	DecisionReason       string
	EstimatedInputTokens int32
	StickyHit            bool
	// PinTier is the actual served-path turn-loop tier. Empty leaves the column NULL.
	PinTier                string
	EmbedInput             string
	InputTokens            int32
	OutputTokens           int32
	RequestedInputCostUSD  float64
	RequestedOutputCostUSD float64
	ActualInputCostUSD     float64
	ActualOutputCostUSD    float64
	RouteLatencyMs         int64
	UpstreamLatencyMs      int64
	TotalLatencyMs         int64
	CrossFormat            bool
	UpstreamStatusCode     int32

	ClusterIDs           []int32
	CandidateModels      []string
	ChosenScore          *float64
	AlphaBreakdown       []byte // pre-marshaled JSON for W-1335; nil until then
	CandidateScores      []byte // pre-marshaled JSON model->score; nil for non-score routers
	Propensity           *float64
	ClusterRouterVersion string
	// Strategy names the routing model that produced this decision ("cluster",
	// "hmm", "rl", "bandit"). Always populated. Empty leaves the column NULL.
	Strategy string
	// RouteID is the opaque sidecar correlation id (HMM/RL) joining a decision
	// to its outcome report. Empty for the default cluster scorer → NULL column.
	RouteID string
	// Policy fields mirror the versioned sidecar contract. They remain generic
	// so a future strategy is collected without adding a strategy-specific row.
	PolicyRouteKey       string
	PolicyArtifactID     string
	PolicyArtifactSHA256 string
	RosterVersion        string
	SidecarSchemaVersion string
	TrainingAllowed      bool
	CaptureMode          string
	// DebugRef is populated only when authorized policy debug mode is enabled.
	DebugRef            string
	TTFTMs              *int64
	CacheCreationTokens *int32
	CacheReadTokens     *int32
	DeviceID            string
	SessionID           string
	RouterUserID        string
	ClientApp           string
	TurnType            string
	// RolloutID joins eval/training-harness rollout rewards onto decisions
	// (x-weave-rollout-id header). Empty for normal traffic → NULL column.
	RolloutID string

	UpstreamFinishReason  *string
	StopReason            *string
	ToolUseBlocks         *int32
	InvalidToolArgsBlocks *int32
	FailoverUsed          *bool
	DegenerateShadow      *bool

	// SessionKey + Role are the offline join key to spiral_shadow_events and
	// session_pins (16-byte digest + roleForTier of the requested model). Nil /
	// empty leaves the columns NULL.
	SessionKey []byte
	Role       string

	// FreshDecisionModel + FreshCandidateScores capture the scorer's fresh
	// recommendation even on STAY turns (shadow-mode instrumentation for the
	// hysteresis downgrade lever). PinAgeSec supports min-dwell analysis. Empty
	// / nil leaves the columns NULL.
	FreshDecisionModel   string
	FreshCandidateScores []byte
	PinAgeSec            *int64

	// ToolResultBytes is the incoming tool-output size on a tool_result turn
	// (shadow-mode instrumentation for the tier-cap lever). nil when the turn
	// carries no trailing tool_result.
	ToolResultBytes *int32

	// CredentialKeyPrefix/CredentialKeySuffix are the safe display parts of the
	// upstream credential that served the turn; CredentialSource names the
	// precedence branch it came from (subscription / codex_subscription / byok /
	// client). Empty on deployment-key turns, leaving the columns NULL. Equal
	// prefix/suffix values across distinct RouterUserIDs reveal one subscription
	// paying for many seats.
	CredentialKeyPrefix string
	CredentialKeySuffix string
	CredentialSource    string

	// UnifiedLimitHeaders is the verbatim anthropic-ratelimit-unified-* header
	// set, pre-marshaled JSON. Phase 0 instrumentation — nil on non-subscription
	// turns. Nothing reads this yet.
	UnifiedLimitHeaders []byte

	// Planner* columns persist the per-turn verdict. nil/empty when planner did not run;
	// a stored zero must not read as evidence. Cost fields are float64 USD; postgres adapter converts to micros.
	PlannerOutcome                  string
	PlannerReason                   string
	PlannerPinModel                 string
	PlannerPinProvider              string
	PlannerExpectedSavingsUSD       *float64
	PlannerEvictionCostUSD          *float64
	PlannerPinCacheCold             *bool
	PlannerShadowOutcome            string
	PlannerShadowExpectedSavingsUSD *float64
	// AuthorityShadow* columns persist the counterfactual cache-gate verdict on
	// authoritative-per-turn turns, where the gate itself never runs. nil/empty
	// when the shadow did not run. Never a served decision.
	AuthorityShadowOutcome             string
	AuthorityShadowWouldDiverge        *bool
	AuthorityShadowReason              string
	AuthorityShadowStayModel           string
	AuthorityShadowStayProvider        string
	AuthorityShadowExpectedSavingsUSD  *float64
	AuthorityShadowEvictionCostUSD     *float64
	AuthorityShadowPinCacheCold        *bool
	AuthorityShadowCorrectedOutcome    string
	AuthorityShadowCorrectedSavingsUSD *float64
	AuthorityShadowStayScore           *float64
	AuthorityShadowFreshScore          *float64
}

// TelemetrySummary holds aggregated totals for the dashboard cards.
type TelemetrySummary struct {
	RequestCount          int64
	TotalTokens           int64
	TotalRequestedCostUSD float64
	TotalActualCostUSD    float64
	TotalSavingsUSD       float64
}

// TelemetryBucket is one time-bucket entry for the cost savings chart.
type TelemetryBucket struct {
	Bucket           time.Time
	RequestedCostUSD float64
	ActualCostUSD    float64
}

// TelemetryModelBucket is one time-bucket entry for a single decision model,
// powering the per-model usage and spend charts.
type TelemetryModelBucket struct {
	Bucket        time.Time
	DecisionModel string
	RequestCount  int64
	TotalTokens   int64
	ActualCostUSD float64
}

// TelemetryRow is one upstream span returned by the drill-down endpoint.
type TelemetryRow struct {
	Timestamp           time.Time
	RequestID           string
	RequestedModel      string
	DecisionModel       string
	DecisionProvider    string
	DecisionReason      string
	StickyHit           bool
	InputTokens         int32
	OutputTokens        int32
	CacheCreationTokens *int32
	CacheReadTokens     *int32
	RequestedCostUSD    float64
	ActualCostUSD       float64
	TotalLatencyMs      int64
	UpstreamStatusCode  int32
	RouterUserID        string
	ClientApp           string
	TurnType            string
	UserEmail           string
}

// TelemetryTurnResult is the per-turn telemetry metadata relevant for
// sequence-based router feedback attribution.
type TelemetryTurnResult struct {
	RequestID        string
	DecisionModel    string
	DecisionProvider string
	RouteID          string
	Strategy         string
	Timestamp        time.Time
}

// applyPlannerTelemetry copies the planner verdict onto p. No-ops when Reason == ""
// so all Planner* columns stay NULL; a stored 0.0 would falsely imply the planner ran.
func applyPlannerTelemetry(p *InsertTelemetryParams, res turnLoopResult) {
	if p == nil || res.PlannerDecision.Reason == "" {
		return
	}
	p.PlannerOutcome = plannerOutcomeAttr(res)
	p.PlannerReason = res.PlannerDecision.Reason
	p.PlannerPinModel = res.PinModel
	p.PlannerPinProvider = res.PinProvider
	savings := res.PlannerDecision.ExpectedSavingsUSD
	eviction := res.PlannerDecision.EvictionCostUSD
	p.PlannerExpectedSavingsUSD = &savings
	p.PlannerEvictionCostUSD = &eviction
	cold := res.PlannerDecision.PinCacheCold
	p.PlannerPinCacheCold = &cold
	if res.PlannerDecision.ShadowComputed {
		p.PlannerShadowOutcome = plannerOutcome(res.PlannerDecision.ShadowOutcome)
		shadow := res.PlannerDecision.ShadowExpectedSavingsUSD
		p.PlannerShadowExpectedSavingsUSD = &shadow
	}
}

// applyAuthorityShadowTelemetry copies the authority-turn cache-gate shadow onto
// p. No-ops unless the shadow ran, keeping all AuthorityShadow* columns NULL
// rather than zero (zero would read as a computed verdict). These never describe
// what was served: an authoritative turn always serves decision_model.
func applyAuthorityShadowTelemetry(p *InsertTelemetryParams, res turnLoopResult) {
	if p == nil || !res.AuthorityShadow.Computed {
		return
	}
	shadow := res.AuthorityShadow
	p.AuthorityShadowOutcome = plannerOutcome(shadow.Decision.Outcome)
	diverge := shadow.Sticky
	p.AuthorityShadowWouldDiverge = &diverge
	p.AuthorityShadowReason = shadow.Decision.Reason
	p.AuthorityShadowStayModel = shadow.StayModel
	p.AuthorityShadowStayProvider = shadow.StayProvider
	p.AuthorityShadowStayScore = shadow.StayScore
	p.AuthorityShadowFreshScore = shadow.FreshScore
	if !shadow.EVRan() {
		// An early exit (no_pin, no_prior_usage, same_model, pricing_missing)
		// carries no cost arithmetic: Decision's float fields are still their zero
		// values and PinCacheCold is documented as meaningless there. Outcome and
		// reason above are real and stay; the EV columns must be NULL, not 0.
		return
	}
	savings := shadow.Decision.ExpectedSavingsUSD
	eviction := shadow.Decision.EvictionCostUSD
	p.AuthorityShadowExpectedSavingsUSD = &savings
	p.AuthorityShadowEvictionCostUSD = &eviction
	cold := shadow.Decision.PinCacheCold
	p.AuthorityShadowPinCacheCold = &cold
	// planner.Decide computes the corrected-economics counterfactual on every EV
	// turn regardless of the deployed config, so the corrected verdict comes for
	// free. It is pre-gate: hmmCostGatedDecision overrides Outcome for a confident
	// upgrade or a same-tier pin and does not mirror those onto ShadowOutcome.
	p.AuthorityShadowCorrectedOutcome = plannerOutcome(shadow.Decision.ShadowOutcome)
	corrected := shadow.Decision.ShadowExpectedSavingsUSD
	p.AuthorityShadowCorrectedSavingsUSD = &corrected
}
