package proxy

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/gitcontext"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/turnrecord"
)

func applyClientGitContextTelemetry(ctx context.Context, params *turnrecord.InsertTelemetryParams, firstTurn bool, systemBlocks []string) {
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
	enabled, _ := ctx.Value(requestcontext.InstallationTrialCaptureContextKey{}).(bool)
	return enabled
}

func applyPolicyPinTelemetry(ctx context.Context, params *turnrecord.InsertTelemetryParams, metadata *router.RoutingMetadata) {
	request, requested := router.PolicyPinRequestFrom(ctx)
	if params == nil || !requested {
		return
	}
	honoured := request.Authorized && metadata != nil && metadata.PolicyPinHonoured
	params.PolicyPinRequested = &requested
	params.PolicyPinHonoured = &honoured
}

func applyBlindExperimentTelemetry(ctx context.Context, params *turnrecord.InsertTelemetryParams, routed *routingpolicy.TurnRoute) {
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
		case routingpolicy.IsUserForcedReason(routed.Decision.Reason):
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

func applyPlannerTelemetry(p *turnrecord.InsertTelemetryParams, res routingpolicy.TurnRoute) {
	if p == nil || !res.PlannerDecision.Evaluated() {
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

func applyEffortTelemetry(p *turnrecord.InsertTelemetryParams, e dispatch.EffortResolution) {
	if p == nil || e.Source == "" {
		return
	}
	p.EffortArm = e.Arm
	p.EffortSelected = e.Selected
	p.EffortSent = e.Sent
	p.EffortSource = e.Source
}

func applyAuthorityShadowTelemetry(p *turnrecord.InsertTelemetryParams, res routingpolicy.TurnRoute) {
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

func int32Ptr(v int32) *int32 {
	return &v
}

type attemptSink struct {
	store  telemetry.InferenceAttemptStore
	logger *slog.Logger
}

func (s attemptSink) RecordAttempt(ctx context.Context, event inference.AttemptEvent) {
	installationID, _ := ctx.Value(requestcontext.InstallationIDContextKey{}).(string)
	if s.store == nil || installationID == "" {
		return
	}
	s.persist(context.WithoutCancel(ctx), installationID, event)
}

func (s attemptSink) persist(ctx context.Context, installationID string, event inference.AttemptEvent) {
	if err := s.store.InsertInferenceAttempt(ctx, telemetry.InsertInferenceAttemptParams{
		InstallationID: installationID,
		Event:          event,
	}); err != nil && s.logger != nil {
		s.logger.Warn("inference attempt persist failed",
			"operation_id", event.OperationID,
			"attempt_index", event.AttemptIndex,
			"error", err)
	}
}

func NewAttemptSink(store telemetry.InferenceAttemptStore, logger *slog.Logger) dispatch.AttemptSink {
	return attemptSink{store: store, logger: logger}
}

const DefaultAttemptQueueCapacity = 1024

type bufferedAttempt struct {
	installationID string
	event          inference.AttemptEvent
}

type AsyncAttemptSink struct {
	inner     attemptSink
	logger    *slog.Logger
	queue     chan bufferedAttempt
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.RWMutex
	closed    bool
	dropped   atomic.Uint64
	warned    atomic.Bool
}

var _ dispatch.AttemptSink = (*AsyncAttemptSink)(nil)

func NewAsyncAttemptSink(store telemetry.InferenceAttemptStore, logger *slog.Logger, capacity int) *AsyncAttemptSink {
	if capacity <= 0 {
		capacity = DefaultAttemptQueueCapacity
	}
	sink := &AsyncAttemptSink{
		inner:  attemptSink{store: store, logger: logger},
		logger: logger,
		queue:  make(chan bufferedAttempt, capacity),
		done:   make(chan struct{}),
	}
	if store == nil {
		close(sink.done)
	} else {
		go sink.drain()
	}
	return sink
}

func (s *AsyncAttemptSink) RecordAttempt(ctx context.Context, event inference.AttemptEvent) {
	installationID, _ := ctx.Value(requestcontext.InstallationIDContextKey{}).(string)
	if s.inner.store == nil || installationID == "" {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.queue <- bufferedAttempt{installationID: installationID, event: event}:
	default:
		s.dropped.Add(1)
		if s.warned.CompareAndSwap(false, true) && s.logger != nil {
			s.logger.Warn("inference attempt queue full; dropping events", "operation_id", event.OperationID)
		}
	}
}

func (s *AsyncAttemptSink) drain() {
	defer close(s.done)
	for attempt := range s.queue {
		s.inner.persist(context.Background(), attempt.installationID, attempt.event)
	}
}

func (s *AsyncAttemptSink) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.queue)
		s.mu.Unlock()
	})
	select {
	case <-s.done:
		if dropped := s.dropped.Load(); dropped > 0 && s.logger != nil {
			s.logger.Warn("inference attempt events dropped", "count", dropped)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const DecisionReasonPolicyPinUnservable = router.PolicyPinUnavailableReason

const DecisionReasonRoutingFailed = "routing_failed"

func plannerOutcome(outcome planner.Outcome) string { return routingpolicy.PlannerOutcome(outcome) }
