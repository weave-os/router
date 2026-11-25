package turnrecord

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/router/switchpolicy"
)

var ErrSessionCostNotFound = errors.New("no committed router telemetry for session")

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
	GetTelemetryDashboardFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryDashboardFact, error)
	GetTelemetryDashboardActiveUsers(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryActiveUsers, error)
	GetTelemetryDashboardPeople(ctx context.Context, installationID string) ([]TelemetryPerson, error)
	GetTelemetrySubscriptionPoolSeats(ctx context.Context, installationID string, servedSince, readingsSince time.Time) ([]TelemetrySubscriptionSeat, error)
	GetTelemetryDashboardDrillDown(ctx context.Context, installationID string, from, to time.Time) ([]TelemetryDrillDownRow, error)
	GetTelemetryDashboardIndividualFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryIndividualFact, error)
}

type SessionCost struct {
	ContextSnapshot        *ContextSnapshot
	SessionID              string
	RequestCount           int64
	ActualCostUSDMicros    int64
	RequestedCostUSDMicros int64
	InputTokens            int64
	OutputTokens           int64
	CacheCreationTokens    int64
	CacheReadTokens        int64
	LastRecordedAt         time.Time
	BlindExperiment        bool
}

type InsertTelemetryParams struct {
	AgreementShadow                 *AgreementShadowInput
	ContextSnapshot                 []byte
	InstallationID                  string
	APIKeyID                        string
	RequestID                       string
	SpanType                        string
	TraceID                         string
	Timestamp                       time.Time
	RequestedModel                  string
	DecisionModel                   string
	DecisionProvider                string
	DecisionReason                  string
	BlindExperimentArm              auth.BlindExperimentArm
	BlindExperimentAssignmentSource auth.BlindExperimentAssignmentSource
	BlindExperimentSubjectKey       string
	BlindExperimentMode             auth.BlindExperimentMode
	BlindExperimentSessionKey       string
	CohortExperimentID              string
	CohortGroupID                   *int16
	CohortPhaseIndex                *int16
	CohortRevision                  *int32
	CohortScheduledArm              auth.BlindExperimentArm
	CohortTreatmentApplied          *bool
	CohortBypassReason              auth.CohortBypassReason
	BlindExperimentModelSource      auth.BlindExperimentModelSource
	BlindExperimentTreatmentApplied *bool
	BlindExperimentBaselineModel    string
	BlindExperimentModelChanged     *bool
	EstimatedInputTokens            int32
	StickyHit                       bool
	PinTier                         string
	EmbedInput                      string
	InputTokens                     int32
	OutputTokens                    int32
	RequestedInputCostUSD           float64
	RequestedOutputCostUSD          float64
	ActualInputCostUSD              float64
	ActualOutputCostUSD             float64
	RouteLatencyMs                  int64
	UpstreamLatencyMs               int64
	TotalLatencyMs                  int64
	CrossFormat                     bool
	UpstreamStatusCode              int32

	ClusterIDs               []int32
	CandidateModels          []string
	ChosenScore              *float64
	AlphaBreakdown           []byte
	CandidateScores          []byte
	Propensity               *float64
	ClusterRouterVersion     string
	Strategy                 string
	RouteID                  string
	PolicyRouteKey           string
	PolicyArtifactID         string
	PolicyArtifactSHA256     string
	RosterVersion            string
	ClassifierArtifactID     string
	ClassifierArtifactSHA256 string
	ClassifierPredictedLabel string
	ClassifierClassOrder     []string
	ClassifierProbabilities  []byte
	SelectionPolicyReleaseID string
	SelectionPolicySHA256    string
	SelectionHeadGeneration  *int64
	SelectionTrace           []byte
	SidecarSchemaVersion     string
	TrainingAllowed          bool
	CaptureMode              string
	DebugRef                 string
	TTFTMs                   *int64
	CacheCreationTokens      *int32
	CacheCreation1hTokens    *int32
	CacheReadTokens          *int32
	ReasoningTokens          *int32
	Speed                    string
	InferenceGeo             string
	DeviceID                 string
	SessionID                string
	RouterUserID             string
	ClientApp                string
	TurnType                 string
	SubscriberPlan           string
	EntitlementVersion       *int64
	CapacitySource           string
	RetailUsageMicros        *int64
	IncludedUsageMicros      *int64
	LinkedUsageMicros        *int64
	PrepaidUsageMicros       *int64
	SettlementFailed         *bool
	ServingProfileID         string
	ServingProfileVersion    string
	ServingReleaseID         string
	ServingBindingID         string
	ServingTarget            string
	ServingRecipe            ServingRecipe
	BoostOptimizerVersion    string
	RolloutID                string

	UpstreamFinishReason  *string
	StopReason            *string
	ToolUseBlocks         *int32
	InvalidToolArgsBlocks *int32
	LastToolUseName       string
	LastToolUseInputBytes *int32
	ToolErrorCounts       []byte
	AutonomyAppendFired   *bool
	WorkspaceAppendFired  *bool
	FailoverUsed          *bool
	FailoverAttempted     *bool
	DegenerateShadow      *bool
	PolicyPinRequested    *bool
	PolicyPinHonoured     *bool

	SessionKey []byte
	Role       string

	FreshDecisionModel   string
	FreshCandidateScores []byte
	PinAgeSec            *int64

	ToolResultBytes *int32

	CredentialKeyPrefix   string
	CredentialKeySuffix   string
	CredentialSource      string
	SubscriptionAccountID string
	SubscriptionOwnerID   string
	SubscriptionTier      auth.SubscriptionTier
	IntendedModelFamily   string
	FinalModelFamily      string

	UnifiedLimitHeaders []byte
	CodexLimitHeaders   []byte

	PlannerOutcome                     string
	PlannerReason                      string
	PlannerPinModel                    string
	PlannerPinProvider                 string
	PlannerExpectedSavingsUSD          *float64
	PlannerEvictionCostUSD             *float64
	PlannerPinCacheCold                *bool
	PlannerShadowOutcome               string
	PlannerShadowExpectedSavingsUSD    *float64
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

	SpiralErrStreak          *int32
	SpiralErroredResults     *int32
	SpiralToolResults        *int32
	SpiralMaxSameFileEdits   *int32
	SpiralSameFilePathHash   string
	SpiralRepeatFrac         *float64
	SpiralMonologueLen       *int32
	SpiralToolCallCount      *int32
	SpiralMessageCount       *int32
	SpiralPingPongLen        *int32
	SpiralStepsSinceProgress *int32
	SpiralEditAttempted      *bool
	SpiralReasons            []string
	RequestedAllowedModels   []string

	Inference *inference.OperationSummary

	ClientGitHeadSHA string
	ClientGitBranch  string
	ClientGitDirty   *bool

	EffortArm      string
	EffortSelected string
	EffortSent     string
	EffortSource   string

	UserPrompt              *bool
	UserPromptGapMs         *int64
	UserPromptGapPriorModel string
	ErrorClass              TurnErrorClass
	FailureOwner            FailureOwner
	LatestToolCallCounts    []byte
}

type TelemetrySummary struct {
	RequestCount          int64
	TotalTokens           int64
	TotalRequestedCostUSD float64
	TotalActualCostUSD    float64
	TotalSavingsUSD       float64
}

type TelemetryBucket struct {
	Bucket           time.Time
	RequestedCostUSD float64
	ActualCostUSD    float64
}

type TelemetryDashboardFact struct {
	Bucket                 time.Time
	DecisionModel          string
	SubscriptionServed     bool
	DecisionType           string
	ComparisonRoutingModel string
	RequestCount           int64
	InputTokens            int64
	OutputTokens           int64
	CacheCreationTokens    int64
	CacheReadTokens        int64
	ActualCostUSD          float64
}

type TelemetryDrillDownRow struct {
	TelemetryDashboardFact
	PersonID       string
	Linked         bool
	DisplayName    string
	Email          string
	SessionID      string
	ClientApp      string
	RequestedModel string
	Providers      []string
	StartedAt      time.Time
	FinishedAt     time.Time
}

type TelemetryIndividualFact struct {
	TelemetryDashboardFact
	PersonID    string
	Linked      bool
	DisplayName string
	Email       string
}

type TelemetryActiveUsers struct {
	Bucket    time.Time
	Linked    bool
	UserCount int64
}

type TelemetryPerson struct {
	ID            string
	Linked        bool
	DisplayName   string
	Email         string
	LastRequestAt *time.Time
}

type TelemetrySubscriptionSeat struct {
	ID                 string
	Provider           string
	DisplayName        *string
	OwnerName          *string
	Enabled            bool
	State              string
	CooldownUntil      *time.Time
	ServedRequests     int64
	LimitsObservedAt   *time.Time
	ClaudeLimitHeaders map[string]string
	CodexLimitHeaders  map[string]string
}

type TelemetryModelBucket struct {
	Bucket        time.Time
	DecisionModel string
	RequestCount  int64
	TotalTokens   int64
	ActualCostUSD float64
}

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

type TelemetryTurnResult struct {
	RequestID        string
	DecisionModel    string
	DecisionProvider string
	RouteID          string
	Strategy         string
	Timestamp        time.Time
}

type AgreementShadowInput struct {
	Key         switchpolicy.ShadowKey
	Observation switchpolicy.ShadowObservation
	Epoch       int
}

type ServingRecipe struct {
	WorkerRevision            string
	WorkerImageDigest         string
	WorkerConfigurationSHA256 string
	PolicySHA256              string
	EffectiveStrategy         string
	ClassifierReleaseID       string
	ClassifierEnabledVariants string
	FlagOverridesSHA256       string
}
