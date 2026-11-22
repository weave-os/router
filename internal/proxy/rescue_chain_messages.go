package proxy

import (
	"context"
	"net/http"
	"slices"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type messagesRescue struct {
	*dispatch.RescueChain
	svc                     rescueServices
	seats                   seatAdmission
	clock                   auth.Clock
	usageObserver           *usage.Observer
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

	compactionHandoverOutcome dispatch.HandoverOutcome

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
	env := m.Env
	routeRes := m.RouteRes
	feats := m.feats
	m.baselineModel = m.Routing.BaselineFor(ctx, feats.Model)
	baselineCatalog, baselineKnown := catalog.ByID(m.baselineModel)
	_, m.ExplicitRequestedModel = catalog.ByID(m.Request.RequestedModel)
	_, anthropicExcluded := m.Routing.ExcludedProvidersForRequest(ctx)[providers.ProviderAnthropic]
	baselineAllowed := modelPermittedByAllowlist(ctx, m.baselineModel) &&
		modelInRequestSubset(ctx, m.baselineModel)
	m.baselineViable = !m.agentShadowMode &&
		!routeRes.CallerModelPassthrough &&
		!m.Decision.UserForcedModel() &&
		m.Routing.ShouldFailover(ctx) &&
		!paidFallbackForbiddenForModel(ctx, m.baselineModel) &&
		!anthropicExcluded &&
		!weaveCreditsCannotFund(ctx, providers.ProviderAnthropic) &&
		baselineAllowed &&
		m.Decision.Provider != providers.ProviderAnthropic &&
		m.baselineModel != m.Decision.Model &&
		baselineKnown && baselineCatalog.PrimaryProvider() == providers.ProviderAnthropic &&
		siblingFitsContext(nil, m.baselineModel, providers.ProviderAnthropic, m.OverflowEstimate, env.SignatureTokenSavings(), m.OutputReserve) &&
		!slices.Contains(observedOverflowModelsFromContext(ctx), m.baselineModel)
	m.baselineEligible = !routeRes.AuthoritativePerTurn && m.baselineViable
}

func (m *messagesRescue) armSubscriptionRetry(ctx context.Context) {
	m.subscriptionRetryEligible = m.Decision.Provider == providers.ProviderAnthropic &&
		!m.agentShadowMode &&
		servedOnSubscription(ctx) &&
		!paidFallbackForbiddenForModelOnProvider(ctx, m.Decision.Provider, m.Decision.Model) &&
		m.Credentials.AnthropicFallbackKeyAvailable(ctx)
}

func (m *messagesRescue) armRescues(ctx context.Context) context.Context {
	return m.ArmSiblings(ctx, m.agentShadowMode || m.RouteRes.CallerModelPassthrough || m.Decision.UserForcedModel())
}

func (m *messagesRescue) armEarlyStop(ctx context.Context) {
	s := m.svc
	env := m.Env
	routeRes := m.RouteRes
	m.earlyStopMode = s.earlyStopRedoMode(ctx, routeRes, m.Decision, env.Stream(), m.agentShadowMode)
	if m.earlyStopMode != flags.EarlyStopRedoOff {
		basis := rescueBasisForTurn(m.Decision, routeRes)
		target, found := m.Routing.EarlyStopRedoTarget(ctx, basis, m.OverflowEstimate, env.SignatureTokenSavings(), m.OutputReserve)
		if found && m.Routing.SiblingRescueAllowed(ctx, m.Routing.GatewaySiblingAllowed(ctx, target)) && !paidFallbackForbiddenForModel(ctx, target.Model) {
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
	r := m.HTTPRequest
	log := m.Log
	env := m.Env
	routeRes := m.RouteRes
	feats := m.feats
	m.requestedWindowPassed = routeRes.TurnType != turntype.Compaction &&
		pastRequestedWindow(clientRequestedModel(ctx, feats.Model), m.Decision.Model, m.OverflowEstimate, m.OutputReserve)
	if m.attemptBuildErr != nil {
		m.WinnerIdx, m.ProxyErr = -1, m.attemptBuildErr
	} else {
		m.DispatchPrimaryAttempt(ctx, m.baselineViable || m.subscriptionRetryEligible || m.RescueArmed, func(target router.Decision) {
			if !m.agentShadowMode && !routeRes.AuthoritativePerTurn && target.Provider != providers.ProviderAnthropic && !routeRes.HardPinned && !routeRes.HandoverInvoked && routeRes.PrefixTrimmed {
				log.Info("Context trimming detected on selected exhausted-state route; rewriting context with handover summary", "decision_model", target.Model)
				m.compactionHandoverOutcome = s.runCompactionHandover(ctx, env, r.Header, target.Model, *m.Request)
			}
		})
	}
	if m.SubscriptionStatePaidTargetAttempted {
		m.subscriptionRetryEligible = false
	}
}

func (m *messagesRescue) flushErrAsAnthropic(w http.ResponseWriter, err error) {
	env := m.Env
	preludeBuf := m.PreludeBuf
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
	r := m.HTTPRequest
	log := m.Log
	routeRes := m.RouteRes
	opts := m.Opts
	preludeBuf := m.PreludeBuf
	if m.earlyStopMode != flags.EarlyStopRedoOff {
		stopped := m.ProxyErr == nil && m.earlyStop.EarlyStop()
		if releaseCause := m.earlyStop.ReleaseReason(); !stopped && releaseCause != "" {
			log.Debug("Early-stop gate released the turn", "release_cause", releaseCause, "served_model", m.Decision.Model)
		}
		if stopped {
			log.Info("Tool-result turn ended without a tool call below the top candidate tier",
				"served_model", m.Decision.Model,
				"redo_model", m.earlyStopTarget.Model,
				"early_stop_redo_mode", string(m.earlyStopMode),
				"withheld", m.earlyStop.Withheld())
		}
		if stopped && m.earlyStop.Withheld() && !preludeBuf.Committed() {
			m.earlyStopRedone, m.ProxyErr = s.redoEarlyStop(ctx, dispatch.EarlyStopRedo{
				Gate:    m.earlyStop,
				Prelude: preludeBuf,
				Header:  m.ContentSink.Header(),
				Log:     log,
				Target:  m.earlyStopTarget,
				Build: func(ctx context.Context, target router.Decision) (dispatch.UpstreamAttempt, []catalog.ProviderBinding, string, dispatch.EffortResolution, context.Context, error) {
					redoOpts, redoEffort := switchedAttemptOptions(ctx, opts, target, routeRes.EscalateEffort, s.resolveEffort)
					redoCtx := m.Credentials.ResolveCredentials(subscriptionStatePaidRescueContext(rescueCredentialContext(ctx, target.Provider), target.Model), target.Provider, target.Model, r.Header)
					redoOpts.FastMode = fastModeForAttempt(redoCtx, target.Model, target.Provider)
					redoMarker := m.Markers.EarlyStop(ctx, target.Model)
					redoAttempt, buildErr := m.BuildAttempt(target, redoOpts, redoMarker, redoCtx)
					return redoAttempt, m.Routing.ResolveBindingsForDispatch(redoCtx, target), redoMarker, redoEffort, redoCtx, buildErr
				},
				Dispatch: func(redoCtx context.Context, target router.Decision, redoBindings []catalog.ProviderBinding, redoAttempt dispatch.UpstreamAttempt) (int, error) {
					return s.dispatchWithFallback(redoCtx, dispatch.FailoverInputs{
						Writer:                 m.ContentSink,
						Prelude:                preludeBuf,
						InitialDecision:        target,
						Bindings:               redoBindings,
						Attempt:                redoAttempt,
						FlushErr:               m.flushErrAsAnthropic,
						DeferFlushOnExhaustion: true,
						Purpose:                routeRes.DispatchPurpose(inference.PurposeAnthropicMessages),
						Origin:                 routeRes.RescueOrigin(),
					})
				},
				Served: &dispatch.EarlyStopServed{
					Decision:   &m.Decision,
					Bindings:   &m.Bindings,
					Marker:     &m.Marker,
					WinnerIdx:  &m.WinnerIdx,
					Extractor:  m.extractor,
					Summary:    m.RespSummary,
					ReqStats:   m.ReqStats,
					FastServed: m.fastServed,
					Effort:     &m.EffortServed,
				},
			})
			if m.earlyStopRedone {
				recordTurnFailure(ctx, m.PrimaryModel)
				for _, model := range lowerTierCandidates(m.earlyStopTarget.Model, m.earlyStopCandidates) {
					recordTurnFailure(ctx, model)
				}
			}
		}
		if releaseErr := m.earlyStop.Disarm(); releaseErr != nil && m.ProxyErr == nil {
			m.ProxyErr = releaseErr
		}
	}
}

func (m *messagesRescue) flushDeferredErr() {
	env := m.Env
	preludeBuf := m.PreludeBuf
	if m.DeferredErrFlushed {
		return
	}
	m.DeferredErrFlushed = true
	m.ProxyErr = asRequestedWindowOverflow(m.ProxyErr, m.requestedWindowPassed, preludeBuf.Committed())
	if preludeBuf.Committed() {
		m.streamCut.noteCut(m.ProxyErr)
	}
	if env.Stream() && preludeBuf.PreludeSent() {
		sseErr := emitAnthropicSSEErrorEvent(m.ContentSink, m.ProxyErr)
		if !m.SubscriptionPoolFailure {
			m.ProxyErr = sseErr
		}
		return
	}
	if m.SubscriptionPoolFailure {
		return
	}
	flushUpstreamErrorAsAnthropic(m.ContentSink, m.ProxyErr)
}

func (m *messagesRescue) classifyPrimaryFailure() {
	log := m.Log
	feats := m.feats
	m.capabilityRejected = providers.IsUpstreamCapabilityRejection(m.ProxyErr)
	m.CrossBindingRejected = providers.IsUpstreamSchemaRejection(m.ProxyErr) || providers.IsUpstreamThoughtSignatureRejection(m.ProxyErr)
	m.deadArmRejected = m.capabilityRejected || m.CrossBindingRejected || translate.IsIntrinsicallyIncompatible(m.ProxyErr)
	if m.capabilityRejected {
		log.Error("Upstream rejected the request as unsupported by the routed model",
			"model", m.Decision.Model,
			"provider", m.PrimaryProvider,
			"upstream_status", upstreamStatus(m.ProxyErr),
			"has_images", feats.HasImages)
	}
}

func (m *messagesRescue) finish(ctx context.Context) {
	routeRes := m.RouteRes
	preludeBuf := m.PreludeBuf
	m.ProxyErr = asCompactionUnservable(routeRes.TurnType, m.Decision, m.ProxyErr)
	m.ProxyErr = withClientTrimCounts(ctx, m.ProxyErr, m.RescuedPrimaryErr)
	if (m.SiblingRescueOwed || m.OverflowRescueOnly) && m.ProxyErr != nil && !preludeBuf.Committed() {
		m.flushDeferredErr()
	}
	m.ProxyErr = asRequestedWindowOverflow(m.ProxyErr, m.requestedWindowPassed, preludeBuf.Committed())

	m.subscriptionFailoverUsed = m.subscriptionFailoverUsed || subscriptionCredentialFallbackUsed(ctx)
	unservedProvider := m.PrimaryProvider
	if m.BaselineAttempted {
		unservedProvider = providers.ProviderAnthropic
	}
	m.FinalProvider = m.ServedProvider(unservedProvider)
	m.Decision.Provider = m.FinalProvider
	m.FailoverUsed = m.FinalProvider != m.PrimaryProvider || m.subscriptionFailoverUsed || m.SiblingFailoverUsed || m.earlyStopRedone
	m.FailoverAttempted = m.FailoverUsed || m.BaselineAttempted || m.subscriptionFailoverAttempted || m.SiblingRescueRan
}
