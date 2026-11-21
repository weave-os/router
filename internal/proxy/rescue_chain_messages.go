package proxy

import (
	"context"
	"net/http"
	"slices"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type messagesRescue struct {
	*rescueChain
	feats                   translate.RoutingFeatures
	agentShadowMode         bool
	earlyStop               *dispatch.EarlyStopGate
	streamCut               *streamCutObserver
	attemptBuildErr         error
	anthropicTierAttemptFor func(targetOpts translate.EmitOptions, prep providers.PreparedRequest, targetNotice translate.RouterNotice) *anthropicTierAttempt
	recordFastServed        func(fast bool)
	extractor               **telemetry.UsageExtractor
	fastServed              *bool
	crossFormat             *bool

	compactionHandoverOutcome handoverOutcome

	baselineModel             string
	baselineViable            bool
	baselineEligible          bool
	subscriptionRetryEligible bool
	earlyStopMode             flags.EarlyStopRedoMode
	earlyStopTarget           router.Decision
	earlyStopCandidates       []string
	requestedWindowPassed     bool

	earlyStopRedone               bool
	baselineFailoverUsed          bool
	capabilityRejected            bool
	deadArmRejected               bool
	subscriptionFailoverUsed      bool
	subscriptionRetryRan          bool
	subscriptionFailoverAttempted bool
}

func (m *messagesRescue) armBaseline(ctx context.Context) {
	s := m.svc
	env := m.env
	routeRes := m.routeRes
	feats := m.feats
	m.baselineModel = s.baselineFor(ctx, feats.Model)
	baselineCatalog, baselineKnown := catalog.ByID(m.baselineModel)
	_, m.explicitRequestedModel = catalog.ByID(m.req.RequestedModel)
	_, anthropicExcluded := s.excludedProvidersForRequest(ctx)[providers.ProviderAnthropic]
	baselineAllowed := modelPermittedByAllowlist(ctx, m.baselineModel) &&
		modelInRequestSubset(ctx, m.baselineModel)
	m.baselineViable = !m.agentShadowMode &&
		!routeRes.CallerModelPassthrough &&
		!m.decision.UserForcedModel() &&
		s.shouldFailover(ctx) &&
		!paidFallbackForbiddenForModel(ctx, m.baselineModel) &&
		!anthropicExcluded &&
		!weaveCreditsCannotFund(ctx, providers.ProviderAnthropic) &&
		baselineAllowed &&
		m.decision.Provider != providers.ProviderAnthropic &&
		m.baselineModel != m.decision.Model &&
		baselineKnown && baselineCatalog.PrimaryProvider() == providers.ProviderAnthropic &&
		siblingFitsContext(nil, m.baselineModel, providers.ProviderAnthropic, m.overflowEstimate, env.SignatureTokenSavings(), m.outputReserve) &&
		!slices.Contains(observedOverflowModelsFromContext(ctx), m.baselineModel)
	m.baselineEligible = !routeRes.AuthoritativePerTurn && m.baselineViable
}

func (m *messagesRescue) armSubscriptionRetry(ctx context.Context) {
	s := m.svc
	m.subscriptionRetryEligible = m.decision.Provider == providers.ProviderAnthropic &&
		!m.agentShadowMode &&
		servedOnSubscription(ctx) &&
		!paidFallbackForbiddenForModelOnProvider(ctx, m.decision.Provider, m.decision.Model) &&
		s.anthropicFallbackKeyAvailable(ctx)
}

func (m *messagesRescue) armRescues(ctx context.Context) context.Context {
	return m.armSiblings(ctx, m.agentShadowMode || m.routeRes.CallerModelPassthrough || m.decision.UserForcedModel())
}

func (m *messagesRescue) armEarlyStop(ctx context.Context) {
	s := m.svc
	env := m.env
	routeRes := m.routeRes
	m.earlyStopMode = s.earlyStopRedoMode(ctx, routeRes, m.decision, env.Stream(), m.agentShadowMode)
	if m.earlyStopMode != flags.EarlyStopRedoOff {
		basis := rescueBasisForTurn(m.decision, routeRes)
		target, found := s.earlyStopRedoTarget(ctx, basis, m.overflowEstimate, env.SignatureTokenSavings(), m.outputReserve)
		if found && s.siblingRescueAllowed(ctx, s.gatewaySiblingAllowed(ctx, target)) && !paidFallbackForbiddenForModel(ctx, target.Model) {
			m.earlyStopTarget = target
			m.earlyStopCandidates = basis.Metadata.CandidateModels
			m.earlyStop.Observe(m.earlyStopMode == flags.EarlyStopRedoOn)
		} else {
			m.earlyStopMode = flags.EarlyStopRedoOff
		}
	}
}

func (m *messagesRescue) dispatchPrimary(ctx context.Context) {
	s := m.svc
	r := m.r
	log := m.log
	env := m.env
	routeRes := m.routeRes
	feats := m.feats
	m.requestedWindowPassed = routeRes.TurnType != turntype.Compaction &&
		pastRequestedWindow(clientRequestedModel(ctx, feats.Model), m.decision.Model, m.overflowEstimate, m.outputReserve)
	if m.attemptBuildErr != nil {
		m.winnerIdx, m.proxyErr = -1, m.attemptBuildErr
	} else {
		m.dispatchPrimaryAttempt(ctx, m.baselineViable || m.subscriptionRetryEligible || m.rescueArmed, func(target router.Decision) {
			if !m.agentShadowMode && !routeRes.AuthoritativePerTurn && target.Provider != providers.ProviderAnthropic && !routeRes.HardPinned && !routeRes.Handover.Invoked && routeRes.PrefixTrimmed {
				log.Info("Context trimming detected on selected exhausted-state route; rewriting context with handover summary", "decision_model", target.Model)
				m.compactionHandoverOutcome = s.runCompactionHandover(ctx, env, r.Header, target.Model, *m.req)
			}
		})
	}
	if m.subscriptionStatePaidTargetAttempted {
		m.subscriptionRetryEligible = false
	}
}

func (m *messagesRescue) flushErrAsAnthropic(w http.ResponseWriter, err error) {
	env := m.env
	preludeBuf := m.preludeBuf
	err = asRequestedWindowOverflow(err, m.requestedWindowPassed, preludeBuf.Committed())
	if env.Stream() && preludeBuf.PreludeSent() {
		_ = emitAnthropicSSEErrorEvent(w, err)
		return
	}
	if isSubscriptionPoolError(err) {
		return
	}
	flushUpstreamErrorAsAnthropic(w, err)
}

func (m *messagesRescue) redoIfStoppedEarly(ctx context.Context) {
	s := m.svc
	r := m.r
	log := m.log
	routeRes := m.routeRes
	opts := m.opts
	preludeBuf := m.preludeBuf
	if m.earlyStopMode != flags.EarlyStopRedoOff {
		stopped := m.proxyErr == nil && m.earlyStop.EarlyStop()
		if releaseCause := m.earlyStop.ReleaseReason(); !stopped && releaseCause != "" {
			log.Debug("Early-stop gate released the turn", "release_cause", releaseCause, "served_model", m.decision.Model)
		}
		if stopped {
			log.Info("Tool-result turn ended without a tool call below the top candidate tier",
				"served_model", m.decision.Model,
				"redo_model", m.earlyStopTarget.Model,
				"early_stop_redo_mode", string(m.earlyStopMode),
				"withheld", m.earlyStop.Withheld())
		}
		if stopped && m.earlyStop.Withheld() && !preludeBuf.Committed() {
			m.earlyStopRedone, m.proxyErr = s.redoEarlyStop(ctx, earlyStopRedo{
				gate:    m.earlyStop,
				prelude: preludeBuf,
				header:  m.contentSink.Header(),
				log:     log,
				target:  m.earlyStopTarget,
				build: func(ctx context.Context, target router.Decision) (dispatchAttempt, []catalog.ProviderBinding, string, effortResolution, context.Context, error) {
					redoOpts, redoEffort := switchedAttemptOptions(ctx, opts, target, routeRes.EscalateEffort, s.resolveEffort)
					redoCtx := s.resolveCredentials(subscriptionStatePaidRescueContext(rescueCredentialContext(ctx, target.Provider), target.Model), target.Provider, target.Model, r.Header)
					redoOpts.FastMode = fastModeForAttempt(redoCtx, target.Model, target.Provider)
					redoMarker := suppressMarkerIfRequested(ctx, r.Header, modelSelectionMarkerForRequest(m.strategyPorts, ctx, routeRes, siblingRoutingMarkerFor(routeRes, markerReasonEarlyStop, target.Model), target.Model, markerReasonEarlyStop))
					redoAttempt, buildErr := m.buildAttempt(target, redoOpts, redoMarker, redoCtx)
					return redoAttempt, s.resolveBindingsForDispatch(redoCtx, target), redoMarker, redoEffort, redoCtx, buildErr
				},
				dispatch: func(redoCtx context.Context, target router.Decision, redoBindings []catalog.ProviderBinding, redoAttempt dispatchAttempt) (int, error) {
					return s.dispatchWithFallback(redoCtx, failoverInputs{
						w:                      m.contentSink,
						buf:                    preludeBuf,
						initialDecision:        target,
						bindings:               redoBindings,
						attempt:                redoAttempt,
						flushErr:               m.flushErrAsAnthropic,
						deferFlushOnExhaustion: true,
						purpose:                routeRes.dispatchPurpose(inference.PurposeAnthropicMessages),
						origin:                 routeRes.rescueOrigin(),
					})
				},
				served: &earlyStopServed{
					decision:   &m.decision,
					bindings:   &m.bindings,
					marker:     &m.marker,
					winnerIdx:  &m.winnerIdx,
					extractor:  m.extractor,
					summary:    m.respSummary,
					reqStats:   m.reqStats,
					fastServed: m.fastServed,
					effort:     &m.effortServed,
				},
			})
			if m.earlyStopRedone {
				recordTurnFailure(ctx, m.primaryModel)
				for _, model := range lowerTierCandidates(m.earlyStopTarget.Model, m.earlyStopCandidates) {
					recordTurnFailure(ctx, model)
				}
			}
		}
		if releaseErr := m.earlyStop.Disarm(); releaseErr != nil && m.proxyErr == nil {
			m.proxyErr = releaseErr
		}
	}
}

func (m *messagesRescue) flushDeferredErr() {
	env := m.env
	preludeBuf := m.preludeBuf
	if m.deferredErrFlushed {
		return
	}
	m.deferredErrFlushed = true
	m.proxyErr = asRequestedWindowOverflow(m.proxyErr, m.requestedWindowPassed, preludeBuf.Committed())
	if preludeBuf.Committed() {
		m.streamCut.noteCut(m.proxyErr)
	}
	if env.Stream() && preludeBuf.PreludeSent() {
		sseErr := emitAnthropicSSEErrorEvent(m.contentSink, m.proxyErr)
		if !m.subscriptionPoolFailure {
			m.proxyErr = sseErr
		}
		return
	}
	if m.subscriptionPoolFailure {
		return
	}
	flushUpstreamErrorAsAnthropic(m.contentSink, m.proxyErr)
}

func (m *messagesRescue) classifyPrimaryFailure() {
	log := m.log
	feats := m.feats
	m.capabilityRejected = providers.IsUpstreamCapabilityRejection(m.proxyErr)
	m.crossBindingRejected = providers.IsUpstreamSchemaRejection(m.proxyErr) || providers.IsUpstreamThoughtSignatureRejection(m.proxyErr)
	m.deadArmRejected = m.capabilityRejected || m.crossBindingRejected || translate.IsIntrinsicallyIncompatible(m.proxyErr)
	if m.capabilityRejected {
		log.Error("Upstream rejected the request as unsupported by the routed model",
			"model", m.decision.Model,
			"provider", m.primaryProvider,
			"upstream_status", upstreamStatus(m.proxyErr),
			"has_images", feats.HasImages)
	}
}

func (m *messagesRescue) finish(ctx context.Context) {
	routeRes := m.routeRes
	preludeBuf := m.preludeBuf
	m.proxyErr = asCompactionUnservable(routeRes.TurnType, m.decision, m.proxyErr)
	m.proxyErr = withClientTrimCounts(ctx, m.proxyErr, m.rescuedPrimaryErr)
	if (m.siblingRescueOwed || m.overflowRescueOnly) && m.proxyErr != nil && !preludeBuf.Committed() {
		m.flushDeferredErr()
	}
	m.proxyErr = asRequestedWindowOverflow(m.proxyErr, m.requestedWindowPassed, preludeBuf.Committed())

	m.subscriptionFailoverUsed = m.subscriptionFailoverUsed || subscriptionCredentialFallbackUsed(ctx)
	unservedProvider := m.primaryProvider
	if m.baselineAttempted {
		unservedProvider = providers.ProviderAnthropic
	}
	m.finalProvider = m.servedProvider(unservedProvider)
	m.decision.Provider = m.finalProvider
	m.failoverUsed = m.finalProvider != m.primaryProvider || m.subscriptionFailoverUsed || m.siblingFailoverUsed || m.earlyStopRedone
	m.failoverAttempted = m.failoverUsed || m.baselineAttempted || m.subscriptionFailoverAttempted || m.siblingRescueRan
}
