package dispatch

import (
	"context"
	"net/http"
	"slices"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type MessagesRescue struct {
	*RescueChain
	Feats                translate.RoutingFeatures
	AgentShadowMode      bool
	EarlyStop            *EarlyStopGate
	NoteStreamCut        func(err error)
	AttemptBuildErr      error
	AnthropicTierAttempt func(targetOpts translate.EmitOptions, prep providers.PreparedRequest, targetNotice translate.RouterNotice) UpstreamAttempt
	Extractor            **telemetry.UsageExtractor
	FastServed           *bool
	CrossFormat          *bool

	CompactionHandoverOutcome handover.Outcome

	BaselineModel             string
	BaselineViable            bool
	BaselineEligible          bool
	SubscriptionRetryEligible bool
	EarlyStopMode             flags.EarlyStopRedoMode
	EarlyStopTarget           router.Decision
	EarlyStopCandidates       []string
	RequestedWindowPassed     bool

	EarlyStopRedone               bool
	BaselineFailoverUsed          bool
	CapabilityRejected            bool
	DeadArmRejected               bool
	SubscriptionFailoverUsed      bool
	SubscriptionRetryRan          bool
	SubscriptionFailoverAttempted bool
}

func (m *MessagesRescue) ArmBaseline(ctx context.Context) {
	env := m.Env
	routeRes := m.RouteRes
	feats := m.Feats
	m.BaselineModel = m.Routing.BaselineFor(ctx, feats.Model)
	baselineCatalog, baselineKnown := catalog.ByID(m.BaselineModel)
	_, m.ExplicitRequestedModel = catalog.ByID(m.Request.RequestedModel)
	_, anthropicExcluded := m.Routing.ExcludedProvidersForRequest(ctx)[providers.ProviderAnthropic]
	baselineAllowed := m.Routing.ModelPermittedByAllowlist(ctx, m.BaselineModel) &&
		m.Routing.ModelInRequestSubset(ctx, m.BaselineModel)
	m.BaselineViable = !m.AgentShadowMode &&
		!routeRes.CallerModelPassthrough &&
		!m.Decision.UserForcedModel() &&
		m.Routing.ShouldFailover(ctx) &&
		!m.Funding.PaidFallbackForbidden(ctx, m.BaselineModel) &&
		!anthropicExcluded &&
		!m.Funding.WeaveCreditsCannotFund(ctx, providers.ProviderAnthropic) &&
		baselineAllowed &&
		m.Decision.Provider != providers.ProviderAnthropic &&
		m.BaselineModel != m.Decision.Model &&
		baselineKnown && baselineCatalog.PrimaryProvider() == providers.ProviderAnthropic &&
		m.Routing.SiblingFitsContext(nil, m.BaselineModel, providers.ProviderAnthropic, m.OverflowEstimate, env.SignatureTokenSavings(), m.OutputReserve) &&
		!slices.Contains(m.Overflow.ObservedOverflowModels(ctx), m.BaselineModel)
	m.BaselineEligible = !routeRes.AuthoritativePerTurn && m.BaselineViable
}

func (m *MessagesRescue) ArmSubscriptionRetry(ctx context.Context) {
	m.SubscriptionRetryEligible = m.Decision.Provider == providers.ProviderAnthropic &&
		!m.AgentShadowMode &&
		m.Credentials.ServedOnSubscription(ctx) &&
		!m.Funding.PaidFallbackForbiddenOnProvider(ctx, m.Decision.Provider, m.Decision.Model) &&
		m.Credentials.AnthropicFallbackKeyAvailable(ctx)
}

func (m *MessagesRescue) ArmRescues(ctx context.Context) context.Context {
	return m.ArmSiblings(ctx, m.AgentShadowMode || m.RouteRes.CallerModelPassthrough || m.Decision.UserForcedModel())
}

func (m *MessagesRescue) ArmEarlyStop(ctx context.Context) {
	env := m.Env
	routeRes := m.RouteRes
	m.EarlyStopMode = m.Routing.EarlyStopRedoMode(ctx, routeRes, m.Decision, env.Stream(), m.AgentShadowMode)
	if m.EarlyStopMode != flags.EarlyStopRedoOff {
		basis := m.Routing.RescueBasis(m.Decision, routeRes)
		target, found := m.Routing.EarlyStopRedoTarget(ctx, basis, m.OverflowEstimate, env.SignatureTokenSavings(), m.OutputReserve)
		if found && m.Routing.SiblingRescueAllowed(ctx, m.Routing.GatewaySiblingAllowed(ctx, target)) && !m.Funding.PaidFallbackForbidden(ctx, target.Model) {
			m.EarlyStopTarget = target
			m.EarlyStopCandidates = basis.Metadata.CandidateModels
			m.EarlyStop.Observe(m.EarlyStopMode == flags.EarlyStopRedoOn)
		} else {
			m.EarlyStopMode = flags.EarlyStopRedoOff
		}
	}
}

func (m *MessagesRescue) DispatchPrimary(ctx context.Context) {
	r := m.HTTPRequest
	log := m.Log
	env := m.Env
	routeRes := m.RouteRes
	feats := m.Feats
	m.RequestedWindowPassed = routeRes.TurnType != turntype.Compaction &&
		m.Overflow.PastRequestedWindow(m.Overflow.ClientRequestedModel(ctx, feats.Model), m.Decision.Model, m.OverflowEstimate, m.OutputReserve)
	if m.AttemptBuildErr != nil {
		m.WinnerIdx, m.ProxyErr = -1, m.AttemptBuildErr
	} else {
		m.DispatchPrimaryAttempt(ctx, m.BaselineViable || m.SubscriptionRetryEligible || m.RescueArmed, func(target router.Decision) {
			if !m.AgentShadowMode && !routeRes.AuthoritativePerTurn && target.Provider != providers.ProviderAnthropic && !routeRes.HardPinned && !routeRes.HandoverInvoked && routeRes.PrefixTrimmed {
				log.Info("Context trimming detected on selected exhausted-state route; rewriting context with handover summary", "decision_model", target.Model)
				m.CompactionHandoverOutcome = m.Overflow.RunCompactionHandover(ctx, env, r.Header, target.Model, *m.Request)
			}
		})
	}
	if m.SubscriptionStatePaidTargetAttempted {
		m.SubscriptionRetryEligible = false
	}
}

func (m *MessagesRescue) FlushErrAsAnthropic(w http.ResponseWriter, err error) {
	env := m.Env
	preludeBuf := m.PreludeBuf
	err = m.Overflow.AsRequestedWindowOverflow(err, m.RequestedWindowPassed, preludeBuf.Committed())
	if env.Stream() && preludeBuf.PreludeSent() {
		_ = m.Render.EmitAnthropicSSEError(w, err)
		return
	}
	if m.Credentials.IsSubscriptionPoolError(err) {
		return
	}
	m.Render.FlushAnthropicError(w, err)
}

func (m *MessagesRescue) RedoIfStoppedEarly(ctx context.Context) {
	r := m.HTTPRequest
	log := m.Log
	routeRes := m.RouteRes
	opts := m.Opts
	preludeBuf := m.PreludeBuf
	if m.EarlyStopMode != flags.EarlyStopRedoOff {
		stopped := m.ProxyErr == nil && m.EarlyStop.EarlyStop()
		if releaseCause := m.EarlyStop.ReleaseReason(); !stopped && releaseCause != "" {
			log.Debug("Early-stop gate released the turn", "release_cause", releaseCause, "served_model", m.Decision.Model)
		}
		if stopped {
			log.Info("Tool-result turn ended without a tool call below the top candidate tier",
				"served_model", m.Decision.Model,
				"redo_model", m.EarlyStopTarget.Model,
				"early_stop_redo_mode", string(m.EarlyStopMode),
				"withheld", m.EarlyStop.Withheld())
		}
		if stopped && m.EarlyStop.Withheld() && !preludeBuf.Committed() {
			m.EarlyStopRedone, m.ProxyErr = m.Routing.RedoEarlyStop(ctx, EarlyStopRedo{
				Gate:    m.EarlyStop,
				Prelude: preludeBuf,
				Header:  m.ContentSink.Header(),
				Log:     log,
				Target:  m.EarlyStopTarget,
				Build: func(ctx context.Context, target router.Decision) (UpstreamAttempt, []catalog.ProviderBinding, string, EffortResolution, context.Context, error) {
					redoOpts, redoEffort := m.Attempts.SwitchedOptions(ctx, opts, target, routeRes.EscalateEffort)
					redoCtx := m.Credentials.ResolveCredentials(m.Funding.PaidRescueContext(m.Credentials.RescueCredentialContext(ctx, target.Provider), target.Model), target.Provider, target.Model, r.Header)
					redoOpts.FastMode = m.Attempts.FastMode(redoCtx, target.Model, target.Provider)
					redoMarker := m.Markers.EarlyStop(ctx, target.Model)
					redoAttempt, buildErr := m.BuildAttempt(target, redoOpts, redoMarker, redoCtx)
					return redoAttempt, m.Routing.ResolveBindingsForDispatch(redoCtx, target), redoMarker, redoEffort, redoCtx, buildErr
				},
				Dispatch: func(redoCtx context.Context, target router.Decision, redoBindings []catalog.ProviderBinding, redoAttempt UpstreamAttempt) (int, error) {
					return m.Dispatch(redoCtx, FailoverInputs{
						Writer:                 m.ContentSink,
						Prelude:                preludeBuf,
						InitialDecision:        target,
						Bindings:               redoBindings,
						Attempt:                redoAttempt,
						FlushErr:               m.FlushErrAsAnthropic,
						DeferFlushOnExhaustion: true,
						Purpose:                routeRes.DispatchPurpose(inference.PurposeAnthropicMessages),
						Origin:                 routeRes.RescueOrigin(),
					})
				},
				Served: &EarlyStopServed{
					Decision:   &m.Decision,
					Bindings:   &m.Bindings,
					Marker:     &m.Marker,
					WinnerIdx:  &m.WinnerIdx,
					Extractor:  m.Extractor,
					Summary:    m.RespSummary,
					ReqStats:   m.ReqStats,
					FastServed: m.FastServed,
					Effort:     &m.EffortServed,
				},
			})
			if m.EarlyStopRedone {
				m.Routing.RecordTurnFailure(ctx, m.PrimaryModel)
				for _, model := range m.Routing.LowerTierCandidates(m.EarlyStopTarget.Model, m.EarlyStopCandidates) {
					m.Routing.RecordTurnFailure(ctx, model)
				}
			}
		}
		if releaseErr := m.EarlyStop.Disarm(); releaseErr != nil && m.ProxyErr == nil {
			m.ProxyErr = releaseErr
		}
	}
}

func (m *MessagesRescue) FlushDeferredErr() {
	env := m.Env
	preludeBuf := m.PreludeBuf
	if m.DeferredErrFlushed {
		return
	}
	m.DeferredErrFlushed = true
	m.ProxyErr = m.Overflow.AsRequestedWindowOverflow(m.ProxyErr, m.RequestedWindowPassed, preludeBuf.Committed())
	if preludeBuf.Committed() {
		m.NoteStreamCut(m.ProxyErr)
	}
	if env.Stream() && preludeBuf.PreludeSent() {
		sseErr := m.Render.EmitAnthropicSSEError(m.ContentSink, m.ProxyErr)
		if !m.SubscriptionPoolFailure {
			m.ProxyErr = sseErr
		}
		return
	}
	if m.SubscriptionPoolFailure {
		return
	}
	m.Render.FlushAnthropicError(m.ContentSink, m.ProxyErr)
}

func (m *MessagesRescue) ClassifyPrimaryFailure() {
	log := m.Log
	feats := m.Feats
	m.CapabilityRejected = providers.IsUpstreamCapabilityRejection(m.ProxyErr)
	m.CrossBindingRejected = providers.IsUpstreamSchemaRejection(m.ProxyErr) || providers.IsUpstreamThoughtSignatureRejection(m.ProxyErr)
	m.DeadArmRejected = m.CapabilityRejected || m.CrossBindingRejected || translate.IsIntrinsicallyIncompatible(m.ProxyErr)
	if m.CapabilityRejected {
		log.Error("Upstream rejected the request as unsupported by the routed model",
			"model", m.Decision.Model,
			"provider", m.PrimaryProvider,
			"upstream_status", UpstreamStatus(m.ProxyErr),
			"has_images", feats.HasImages)
	}
}

func (m *MessagesRescue) Finish(ctx context.Context) {
	routeRes := m.RouteRes
	preludeBuf := m.PreludeBuf
	m.ProxyErr = m.Overflow.AsCompactionUnservable(routeRes.TurnType, m.Decision, m.ProxyErr)
	m.ProxyErr = m.Overflow.WithClientTrimCounts(ctx, m.ProxyErr, m.RescuedPrimaryErr)
	if (m.SiblingRescueOwed || m.OverflowRescueOnly) && m.ProxyErr != nil && !preludeBuf.Committed() {
		m.FlushDeferredErr()
	}
	m.ProxyErr = m.Overflow.AsRequestedWindowOverflow(m.ProxyErr, m.RequestedWindowPassed, preludeBuf.Committed())

	m.SubscriptionFailoverUsed = m.SubscriptionFailoverUsed || m.Credentials.SubscriptionCredentialFallbackUsed(ctx)
	unservedProvider := m.PrimaryProvider
	if m.BaselineAttempted {
		unservedProvider = providers.ProviderAnthropic
	}
	m.FinalProvider = m.ServedProvider(unservedProvider)
	m.Decision.Provider = m.FinalProvider
	m.FailoverUsed = m.FinalProvider != m.PrimaryProvider || m.SubscriptionFailoverUsed || m.SiblingFailoverUsed || m.EarlyStopRedone
	m.FailoverAttempted = m.FailoverUsed || m.BaselineAttempted || m.SubscriptionFailoverAttempted || m.SiblingRescueRan
}

func (m *MessagesRescue) BaselineRescue(ctx context.Context) context.Context {
	preludeBuf := m.PreludeBuf
	if m.ProxyErr != nil && !preludeBuf.Committed() &&
		((m.BaselineEligible && (providers.IsRetryable(m.ProxyErr) || providers.IsUpstreamModelNotFound(m.ProxyErr))) ||
			(m.BaselineViable && (m.CapabilityRejected || translate.IsIntrinsicallyIncompatible(m.ProxyErr) || m.CrossBindingRejected))) {
		ctx = m.DispatchBaseline(ctx)
	} else if m.BaselineViable && !m.RescueArmed && m.ProxyErr != nil {
		m.FlushDeferredErr()
	}
	return ctx
}

func (m *MessagesRescue) DispatchBaseline(ctx context.Context) context.Context {
	r := m.HTTPRequest
	log := m.Log
	env := m.Env
	routeRes := m.RouteRes
	opts := m.Opts
	preludeBuf := m.PreludeBuf
	feats := m.Feats
	baselineDecision := m.Decision
	baselineDecision.Model = m.BaselineModel
	baselineDecision.Provider = providers.ProviderAnthropic
	baselineDecision.Effort = ""
	baselineOpts, baselineEffort := m.Attempts.SwitchedOptions(ctx, opts, baselineDecision, routeRes.EscalateEffort)
	baselineOpts.ModelSwitched = m.Routing.BaseModelOf(routeRes.PriorServedModel) != m.BaselineModel ||
		routeRes.SessionEverSwitched
	baselineCtx := m.Funding.PaidRescueContext(ctx, m.BaselineModel)
	baselineSubExhausted := m.Credentials.ClaudeSubscriptionExhausted(ctx, r.Header)
	if baselineSubExhausted {
		baselineCtx = credentials.WithSuppressedClaudeSubscription(baselineCtx)
	}
	baselineCtx = m.Credentials.ResolveCredentials(baselineCtx, providers.ProviderAnthropic, m.BaselineModel, r.Header)
	baselineOpts.FastMode = m.Attempts.FastMode(baselineCtx, m.BaselineModel, providers.ProviderAnthropic)
	baselinePrep, baselineEmitErr := env.PrepareAnthropic(r.Header, baselineOpts)
	if baselineEmitErr != nil {
		log.Error("Baseline failover: emit Anthropic body failed; surfacing original error", "err", baselineEmitErr, "baseline_model", m.BaselineModel)
		if !m.RescueArmed {
			m.FlushDeferredErr()
		}
	} else {
		log.Warn("Baseline failover: retrying requested model on Anthropic",
			"failed_model", m.Decision.Model,
			"failed_provider", m.PrimaryProvider,
			"baseline_model", m.BaselineModel,
			"err", m.ProxyErr)
		if baselineSubExhausted {
			ctx = credentials.WithSuppressedClaudeSubscription(ctx)
		}
		m.EffortServed = baselineEffort
		baselineBindings := m.Routing.ResolveBindingsForDispatch(baselineCtx, baselineDecision)
		baselineMarker := m.Markers.WithFailureDetail(ctx, m.Markers.Baseline(ctx, m.BaselineModel))
		baselineNotice := m.Markers.ClaudeNotice(ctx, baselineOpts.TargetModel, baselineMarker)
		baselineNotice = m.Funding.AddRoutingFundingSource(baselineCtx, providers.ProviderAnthropic, baselineOpts.TargetModel, baselineNotice)
		baselineAttempt := m.AnthropicTierAttempt(baselineOpts, baselinePrep, baselineNotice)
		*m.FastServed = baselineOpts.FastMode
		*m.CrossFormat = false
		*m.RespSummary = translate.ResponseSummary{}
		*m.ReqStats = providers.RequestMutationStats{}
		m.Render.LogUpstreamBody(log, routeRes.SessionKey, baselineDecision, feats, baselinePrep.Body)
		m.WinnerIdx, m.ProxyErr = m.Dispatch(baselineCtx, FailoverInputs{
			Writer:          m.ContentSink,
			Prelude:         preludeBuf,
			InitialDecision: baselineDecision,
			Bindings:        baselineBindings,
			Attempt:         baselineAttempt,
			FlushErr:        m.FlushErrAsAnthropic,
			Purpose:         routeRes.DispatchPurpose(inference.PurposeAnthropicMessages),
			Origin:          routeRes.RescueOrigin(),
		})
		m.SubscriptionPoolFailure = m.Credentials.IsSubscriptionPoolError(m.ProxyErr)
		m.Decision = baselineDecision
		m.Bindings = baselineBindings
		m.BaselineAttempted = true
		m.BaselineFailoverUsed = m.ProxyErr == nil
	}
	return ctx
}

func (m *MessagesRescue) SubscriptionRetry(ctx context.Context) {
	r := m.HTTPRequest
	log := m.Log
	env := m.Env
	routeRes := m.RouteRes
	opts := m.Opts
	preludeBuf := m.PreludeBuf
	feats := m.Feats
	if m.SubscriptionRetryEligible && !m.Funding.PaidFallbackForbiddenOnProvider(ctx, m.Decision.Provider, m.Decision.Model) && !m.BaselineAttempted && m.ProxyErr != nil &&
		!preludeBuf.Committed() &&
		(providers.IsRetryable(m.ProxyErr) || m.Credentials.AnthropicOAuthRejected(m.ProxyErr) || m.Credentials.AnthropicSubscriptionModelRejected(m.ProxyErr) || m.Credentials.AnthropicThirdPartyRefused(m.ProxyErr)) {
		m.SubscriptionRetryRan = true
		m.Credentials.RecordOwnClaudeLoginRejected(ctx, r.Header, m.ProxyErr)
		subCtx := m.Funding.PaidRescueContext(credentials.WithSuppressedClaudeSubscription(ctx), m.Decision.Model)
		subCtx = m.Credentials.ResolveAndInjectCredentials(subCtx, providers.ProviderAnthropic, m.Decision.Model, r.Header)
		subOpts := opts
		subOpts.FastMode = m.Attempts.FastMode(subCtx, m.Decision.Model, providers.ProviderAnthropic)
		subPrep, subEmitErr := env.PrepareAnthropic(r.Header, subOpts)
		if subEmitErr != nil {
			log.Error("Subscription failover: emit Anthropic body failed; surfacing original error", "err", subEmitErr, "model", m.Decision.Model)
			if !m.RescueArmed {
				m.FlushDeferredErr()
			}
		} else if subBindings := m.Routing.ResolveBindingsForDispatch(subCtx, m.Decision); len(subBindings) == 0 {
			log.Warn("Subscription failover: no fallback Anthropic binding available; surfacing original error",
				"model", m.Decision.Model,
				"err", m.ProxyErr,
				"upstream_status", UpstreamStatus(m.ProxyErr))
			if !m.RescueArmed {
				m.FlushDeferredErr()
			}
		} else {
			log.Warn("Subscription failover: subscription rejected the turn, retrying requested model on Weave key",
				"model", m.Decision.Model,
				"err", m.ProxyErr,
				"upstream_status", UpstreamStatus(m.ProxyErr))
			subNotice := m.Markers.ClaudeNotice(m.Markers.WithOwnClaudeRejectionNudge(ctx), subOpts.TargetModel, m.Marker)
			subNotice = m.Funding.AddRoutingFundingSource(subCtx, providers.ProviderAnthropic, subOpts.TargetModel, subNotice)
			subAttempt := m.AnthropicTierAttempt(subOpts, subPrep, subNotice)
			*m.FastServed = subOpts.FastMode
			*m.CrossFormat = false
			*m.RespSummary = translate.ResponseSummary{}
			*m.ReqStats = providers.RequestMutationStats{}
			m.Render.LogUpstreamBody(log, routeRes.SessionKey, m.Decision, feats, subPrep.Body)
			m.SubscriptionFailoverAttempted = true
			subscriptionErr := m.ProxyErr
			m.WinnerIdx, m.ProxyErr = m.Dispatch(subCtx, FailoverInputs{
				Writer:                 m.ContentSink,
				Prelude:                preludeBuf,
				InitialDecision:        m.Decision,
				Bindings:               subBindings,
				Attempt:                subAttempt,
				FlushErr:               m.FlushErrAsAnthropic,
				DeferFlushOnExhaustion: m.RescueArmed,
				Purpose:                routeRes.DispatchPurpose(inference.PurposeAnthropicMessages),
				Origin:                 routeRes.DispatchOrigin(m.Decision),
			})
			m.SubscriptionPoolFailure = m.Credentials.IsSubscriptionPoolError(m.ProxyErr)
			m.Bindings = subBindings
			m.SubscriptionFailoverUsed = m.ProxyErr == nil
			if m.Credentials.CredentialRejectionStands(subscriptionErr, m.ProxyErr, m.Credentials.AnthropicOAuthRejected) {
				m.ProxyErr = subscriptionErr
				m.SubscriptionPoolFailure = false
				m.FlushDeferredErr()
			}
		}
	}
	if m.SubscriptionRetryEligible && !m.RescueArmed && !m.BaselineAttempted && !m.SubscriptionRetryRan && m.ProxyErr != nil && !preludeBuf.Committed() {
		m.FlushDeferredErr()
	}
}
