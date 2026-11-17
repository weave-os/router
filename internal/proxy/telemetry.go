package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/gitcontext"
)

type InstallationIDContextKey struct{}

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

type InferenceAttemptStore interface {
	InsertInferenceAttempt(ctx context.Context, p InsertInferenceAttemptParams) error
}

type InsertInferenceAttemptParams struct {
	InstallationID string
	Event          inference.AttemptEvent
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
	agreementShadow                 *agreementShadowInput
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

func applyClientGitContextTelemetry(ctx context.Context, params *InsertTelemetryParams, firstTurn bool, systemBlocks []string) {
	if params == nil || !firstTurn || !trialCaptureEnabledFromContext(ctx) {
		return
	}
	gc, ok := gitcontext.Parse(systemBlocks)
	if !ok {
		observability.FromContext(ctx).Debug("router.client_git_context_unparsed", "system_blocks", len(systemBlocks))
		return
	}
	params.ClientGitHeadSHA = gc.HeadSHA
	params.ClientGitBranch = gc.Branch
	params.ClientGitDirty = &gc.Dirty
}

func trialCaptureEnabledFromContext(ctx context.Context) bool {
	enabled, _ := ctx.Value(InstallationTrialCaptureContextKey{}).(bool)
	return enabled
}

func applyPolicyPinTelemetry(ctx context.Context, params *InsertTelemetryParams, metadata *router.RoutingMetadata) {
	request, requested := router.PolicyPinRequestFrom(ctx)
	if params == nil || !requested {
		return
	}
	honoured := request.Authorized && metadata != nil && metadata.PolicyPinHonoured
	params.PolicyPinRequested = &requested
	params.PolicyPinHonoured = &honoured
}

func applyBlindExperimentTelemetry(ctx context.Context, params *InsertTelemetryParams, routed *turnLoopResult) {
	state, present := auth.BlindExperimentStatusFrom(ctx)
	if params == nil || !present {
		return
	}
	if state.CohortExperimentID != "" {
		params.CohortExperimentID = state.CohortExperimentID
		if state.CohortGroupID > 0 {
			groupID := int16(state.CohortGroupID)
			params.CohortGroupID = &groupID
		}
		revision := int32(state.CohortRevision)
		params.CohortRevision = &revision
		if !state.Enabled {
			params.CohortBypassReason = auth.CohortBypassDisabled
		} else if state.CohortGroupID == 0 {
			params.CohortBypassReason = auth.CohortBypassUnassigned
		} else if !state.Active {
			params.CohortBypassReason = auth.CohortBypassOutsideWindow
		}
	}
	if !state.Active {
		return
	}
	params.BlindExperimentArm = state.Arm
	params.BlindExperimentAssignmentSource = state.AssignmentSource
	params.BlindExperimentSubjectKey = state.CanonicalSubjectKey
	params.BlindExperimentMode = state.Mode
	if state.Mode == auth.BlindExperimentModeSession && state.SessionSubjectKey == "" {
		params.BlindExperimentMode = auth.BlindExperimentModeUser
	}
	params.BlindExperimentSessionKey = state.SessionSubjectKey
	outcome := blindExperimentOutcomeFor(ctx, state, routed, params.RequestedModel, params.DecisionModel)
	params.BlindExperimentModelSource = outcome.source
	params.BlindExperimentTreatmentApplied = &outcome.treatmentApplied
	params.BlindExperimentBaselineModel = outcome.baselineModel
	params.BlindExperimentModelChanged = outcome.modelChanged
	if state.CohortExperimentID != "" {
		phaseIndex := int16(state.CohortPhaseIndex)
		params.CohortPhaseIndex = &phaseIndex
		params.CohortScheduledArm = state.ScheduledArm
		applied := false
		switch {
		case routed == nil || routed.Decision.Model == "":
			params.CohortBypassReason = auth.CohortBypassNotDispatched
		case routed.UsageBypass:
			params.CohortBypassReason = auth.CohortBypassUsageBypass
		case isUserForcedReason(routed.Decision.Reason):
			params.CohortBypassReason = auth.CohortBypassForceModel
		case routed.HardPinned:
			params.CohortBypassReason = auth.CohortBypassHardPin
		case state.Arm == auth.BlindExperimentArmPassthrough:
			applied = routed.CallerModelPassthrough
			if !applied {
				params.CohortBypassReason = auth.CohortBypassNotDispatched
			}
		default:
			applied = true
		}
		params.CohortTreatmentApplied = &applied
	}
	if state.Arm == auth.BlindExperimentArmPassthrough {
		params.TrainingAllowed = false
	}
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
	Bucket              time.Time
	DecisionModel       string
	SubscriptionServed  bool
	DecisionType        string
	AutomaticAlias      bool
	RequestCount        int64
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	ActualCostUSD       float64
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

func applyEffortTelemetry(p *InsertTelemetryParams, e dispatch.EffortResolution) {
	if p == nil || e.Source == "" {
		return
	}
	p.EffortArm = e.Arm
	p.EffortSelected = e.Selected
	p.EffortSent = e.Sent
	p.EffortSource = e.Source
}

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
		return
	}
	savings := shadow.Decision.ExpectedSavingsUSD
	eviction := shadow.Decision.EvictionCostUSD
	p.AuthorityShadowExpectedSavingsUSD = &savings
	p.AuthorityShadowEvictionCostUSD = &eviction
	cold := shadow.Decision.PinCacheCold
	p.AuthorityShadowPinCacheCold = &cold
	p.AuthorityShadowCorrectedOutcome = plannerOutcome(shadow.Decision.ShadowOutcome)
	corrected := shadow.Decision.ShadowExpectedSavingsUSD
	p.AuthorityShadowCorrectedSavingsUSD = &corrected
}

func turnSignalCaptureAllowed(trainingAllowed bool, capture ContentCaptureMode) bool {
	return trainingAllowed && (capture == CaptureHashed || capture == CaptureFull)
}

func applyTurnSignalTelemetry(
	p *InsertTelemetryParams,
	sig spiralSignals,
	reasons []spiralReason,
	computed bool,
	enabled bool,
	trainingAllowed bool,
	capture ContentCaptureMode,
) {
	if p == nil || !computed || !enabled || !turnSignalCaptureAllowed(trainingAllowed, capture) {
		return
	}
	p.SpiralErrStreak = int32Ptr(int32(sig.errStats.TrailingErrStreak))
	p.SpiralErroredResults = int32Ptr(int32(sig.errStats.Errored))
	p.SpiralToolResults = int32Ptr(int32(sig.errStats.Total))
	p.SpiralMaxSameFileEdits = int32Ptr(int32(sig.maxSameFileEdits))
	p.SpiralSameFilePathHash = sig.sameFilePathHash
	repeatFrac := sig.repeatFrac
	p.SpiralRepeatFrac = &repeatFrac
	p.SpiralMonologueLen = int32Ptr(int32(sig.monologueLen))
	p.SpiralToolCallCount = int32Ptr(int32(sig.toolCallCount))
	p.SpiralMessageCount = int32Ptr(int32(sig.messageCount))
	p.SpiralPingPongLen = int32Ptr(int32(sig.pingPongLen))
	p.SpiralStepsSinceProgress = int32Ptr(int32(sig.stepsSinceProgress))
	editAttempted := sig.editAttempted
	p.SpiralEditAttempted = &editAttempted
	if reasons == nil {
		reasons = []spiralReason{}
	}
	p.SpiralReasons = spiralReasonStrings(reasons)
}

func int32Ptr(v int32) *int32 {
	return &v
}

type attemptSink struct {
	store  InferenceAttemptStore
	logger *slog.Logger
}

func (s attemptSink) RecordAttempt(ctx context.Context, event inference.AttemptEvent) {
	if s.store == nil {
		return
	}
	installationID, _ := ctx.Value(InstallationIDContextKey{}).(string)
	if installationID == "" {
		return
	}
	if err := s.store.InsertInferenceAttempt(context.WithoutCancel(ctx), InsertInferenceAttemptParams{
		InstallationID: installationID,
		Event:          event,
	}); err != nil && s.logger != nil {
		s.logger.Warn("inference attempt persist failed",
			"operation_id", event.OperationID,
			"attempt_index", event.AttemptIndex,
			"error", err)
	}
}

func NewAttemptSink(store InferenceAttemptStore, logger *slog.Logger) dispatch.AttemptSink {
	return attemptSink{store: store, logger: logger}
}

const DecisionReasonPolicyPinUnservable = router.PolicyPinUnavailableReason

const DecisionReasonRoutingFailed = "routing_failed"
