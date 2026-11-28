package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/gitcontext"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/routingpolicy"
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

const (
	DefaultAttemptQueueCapacity = 1024
	attemptDrainWorkers         = 4
)

type bufferedAttempt struct {
	ctx            context.Context
	installationID string
	event          inference.AttemptEvent
}

type AsyncAttemptSink struct {
	inner             attemptSink
	logger            *slog.Logger
	mu                sync.Mutex
	cond              *sync.Cond
	queues            map[string][]bufferedAttempt
	active            map[string]bool
	order             []string
	cursor            int
	pending           int
	capacity          int
	installationLimit int
	closed            bool
	dropped           atomic.Uint64
	done              chan struct{}
	closeOnce         sync.Once
}

var _ dispatch.AttemptSink = (*AsyncAttemptSink)(nil)

func NewAsyncAttemptSink(store telemetry.InferenceAttemptStore, logger *slog.Logger, capacity int) *AsyncAttemptSink {
	return newAsyncAttemptSink(store, logger, capacity, attemptDrainWorkers)
}

func newAsyncAttemptSink(store telemetry.InferenceAttemptStore, logger *slog.Logger, capacity, workers int) *AsyncAttemptSink {
	if capacity <= 0 {
		capacity = DefaultAttemptQueueCapacity
	}
	if workers < 1 {
		workers = 1
	}
	sink := &AsyncAttemptSink{
		inner:             attemptSink{store: store, logger: logger},
		logger:            logger,
		queues:            make(map[string][]bufferedAttempt),
		active:            make(map[string]bool),
		capacity:          capacity,
		installationLimit: max(1, capacity/attemptDrainWorkers),
		done:              make(chan struct{}),
	}
	sink.cond = sync.NewCond(&sink.mu)
	if store == nil {
		close(sink.done)
		return sink
	}
	var running sync.WaitGroup
	for i := 0; i < workers; i++ {
		running.Add(1)
		observability.StartLifecycleWorker(logger, "attempt_sink_drain", func() {
			defer running.Done()
			sink.drain()
		})
	}
	observability.StartLifecycleWorker(logger, "attempt_sink_wait", func() {
		running.Wait()
		close(sink.done)
	})
	return sink
}

func (s *AsyncAttemptSink) RecordAttempt(ctx context.Context, event inference.AttemptEvent) {
	installationID, _ := ctx.Value(requestcontext.InstallationIDContextKey{}).(string)
	if s.inner.store == nil || installationID == "" {
		return
	}
	attempt := bufferedAttempt{
		ctx:            detachedAttemptContext(ctx),
		installationID: installationID,
		event:          event,
	}
	s.mu.Lock()
	queued := s.queues[installationID]
	if s.closed || s.pending >= s.capacity || len(queued) >= s.installationLimit {
		dropped := s.dropped.Add(1)
		closed := s.closed
		s.mu.Unlock()
		s.warnDrop(dropped, installationID, event.OperationID, closed)
		return
	}
	if len(queued) == 0 {
		s.order = append(s.order, installationID)
	}
	s.queues[installationID] = append(queued, attempt)
	s.pending++
	s.cond.Signal()
	s.mu.Unlock()
}

func (s *AsyncAttemptSink) storeQueueLocked(installationID string, queued []bufferedAttempt) {
	if len(queued) == 0 {
		delete(s.queues, installationID)
		s.removeOrderLocked(installationID)
		return
	}
	if cap(queued) > 32 && len(queued) <= cap(queued)/4 {
		queued = append([]bufferedAttempt(nil), queued...)
	}
	s.queues[installationID] = queued
}

func (s *AsyncAttemptSink) removeOrderLocked(installationID string) {
	for i, id := range s.order {
		if id != installationID {
			continue
		}
		s.order = append(s.order[:i], s.order[i+1:]...)
		if i < s.cursor && s.cursor > 0 {
			s.cursor--
		} else if s.cursor >= len(s.order) {
			s.cursor = 0
		}
		return
	}
}

func (s *AsyncAttemptSink) warnDrop(dropped uint64, installationID, operationID string, closed bool) {
	if dropped&(dropped-1) == 0 && s.logger != nil {
		observability.SafeGo(s.logger, 2*time.Second, "attempt_sink_drop_warn", func(context.Context) {
			s.logger.Warn("inference attempt dropped", "dropped", dropped, "installation_id", installationID, "operation_id", operationID, "sink_closed", closed)
		})
	}
}

func detachedAttemptContext(ctx context.Context) context.Context {
	detached := context.Background()
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		detached = trace.ContextWithSpanContext(detached, spanContext)
	}
	if sessionID := observability.ClientSessionIDFromContext(ctx); sessionID != "" {
		detached = observability.WithClientSessionID(detached, sessionID)
	}
	return detached
}

func (s *AsyncAttemptSink) drain() {
	for {
		attempt, ok := s.next()
		if !ok {
			return
		}
		s.persist(attempt)
		s.finish(attempt.installationID)
	}
}

func (s *AsyncAttemptSink) finish(installationID string) {
	s.mu.Lock()
	delete(s.active, installationID)
	if s.closed && s.pending == 0 {
		s.cond.Broadcast()
	} else if s.pending > 0 {
		s.cond.Signal()
	}
	s.mu.Unlock()
}

func (s *AsyncAttemptSink) next() (bufferedAttempt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if attempt, ok := s.popReadyLocked(); ok {
			return attempt, true
		}
		if s.closed && s.pending == 0 {
			return bufferedAttempt{}, false
		}
		s.cond.Wait()
	}
}

func (s *AsyncAttemptSink) popReadyLocked() (bufferedAttempt, bool) {
	checked := 0
	for checked < len(s.order) {
		if s.cursor >= len(s.order) {
			s.cursor = 0
		}
		installationID := s.order[s.cursor]
		queued := s.queues[installationID]
		if len(queued) == 0 {
			delete(s.queues, installationID)
			s.removeOrderLocked(installationID)
			continue
		}
		if s.active[installationID] {
			s.cursor++
			checked++
			continue
		}
		attempt := queued[0]
		queued[0] = bufferedAttempt{}
		queued = queued[1:]
		s.pending--
		s.active[installationID] = true
		if len(queued) > 0 {
			s.cursor++
		}
		s.storeQueueLocked(installationID, queued)
		return attempt, true
	}
	return bufferedAttempt{}, false
}

func (s *AsyncAttemptSink) persist(attempt bufferedAttempt) {
	defer func() {
		if r := recover(); r != nil && s.logger != nil {
			s.logger.Error("Inference attempt persist panicked", "panic", r, "operation_id", attempt.event.OperationID)
		}
	}()
	s.inner.persist(attempt.ctx, attempt.installationID, attempt.event)
}

func (s *AsyncAttemptSink) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	select {
	case <-s.done:
		if dropped := s.dropped.Load(); dropped > 0 && s.logger != nil {
			s.logger.Warn("inference attempt events dropped", "count", dropped)
		}
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		queued := s.pending
		inFlight := len(s.active)
		s.mu.Unlock()
		return fmt.Errorf("attempt sink drain timed out (queued=%d in_flight=%d dropped=%d): %w", queued, inFlight, s.dropped.Load(), ctx.Err())
	}
}

const DecisionReasonPolicyPinUnservable = router.PolicyPinUnavailableReason

const DecisionReasonRoutingFailed = "routing_failed"

func plannerOutcome(outcome planner.Outcome) string { return routingpolicy.PlannerOutcome(outcome) }
