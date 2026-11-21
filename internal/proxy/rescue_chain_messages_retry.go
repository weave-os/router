package proxy

import (
	"context"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

func (m *messagesRescue) baselineRescue(ctx context.Context) context.Context {
	preludeBuf := m.preludeBuf
	if m.proxyErr != nil && !preludeBuf.Committed() &&
		((m.baselineEligible && (providers.IsRetryable(m.proxyErr) || providers.IsUpstreamModelNotFound(m.proxyErr))) ||
			(m.baselineViable && (m.capabilityRejected || translate.IsIntrinsicallyIncompatible(m.proxyErr) || m.crossBindingRejected))) {
		ctx = m.dispatchBaseline(ctx)
	} else if m.baselineViable && !m.rescueArmed && m.proxyErr != nil {
		m.flushDeferredErr()
	}
	return ctx
}

func (m *messagesRescue) dispatchBaseline(ctx context.Context) context.Context {
	s := m.svc
	r := m.r
	log := m.log
	env := m.env
	routeRes := m.routeRes
	opts := m.opts
	preludeBuf := m.preludeBuf
	feats := m.feats
	baselineDecision := m.decision
	baselineDecision.Model = m.baselineModel
	baselineDecision.Provider = providers.ProviderAnthropic
	baselineDecision.Effort = ""
	baselineOpts, baselineEffort := switchedAttemptOptions(ctx, opts, baselineDecision, routeRes.EscalateEffort, s.resolveEffort)
	baselineOpts.ModelSwitched = baseModelOf(routeRes.PriorServedModel) != m.baselineModel ||
		routeRes.SessionEverSwitched
	baselineCtx := subscriptionStatePaidRescueContext(ctx, m.baselineModel)
	baselineSubExhausted := m.credentials.ClaudeSubscriptionExhausted(ctx, r.Header)
	if baselineSubExhausted {
		baselineCtx = withSuppressedClaudeSubscription(baselineCtx)
	}
	baselineCtx = m.credentials.ResolveCredentials(baselineCtx, providers.ProviderAnthropic, m.baselineModel, r.Header)
	baselineOpts.FastMode = fastModeForAttempt(baselineCtx, m.baselineModel, providers.ProviderAnthropic)
	baselinePrep, baselineEmitErr := env.PrepareAnthropic(r.Header, baselineOpts)
	if baselineEmitErr != nil {
		log.Error("Baseline failover: emit Anthropic body failed; surfacing original error", "err", baselineEmitErr, "baseline_model", m.baselineModel)
		if !m.rescueArmed {
			m.flushDeferredErr()
		}
	} else {
		log.Warn("Baseline failover: retrying requested model on Anthropic",
			"failed_model", m.decision.Model,
			"failed_provider", m.primaryProvider,
			"baseline_model", m.baselineModel,
			"err", m.proxyErr)
		if baselineSubExhausted {
			ctx = withSuppressedClaudeSubscription(ctx)
		}
		m.effortServed = baselineEffort
		baselineBindings := m.routing.ResolveBindingsForDispatch(baselineCtx, baselineDecision)
		baselineMarker := m.markers.WithFailureDetail(ctx, m.markers.Baseline(ctx, m.baselineModel))
		baselineNotice := m.markers.ClaudeNotice(ctx, baselineOpts.TargetModel, baselineMarker)
		baselineNotice = addRoutingFundingSource(baselineCtx, m.seats, providers.ProviderAnthropic, baselineOpts.TargetModel, baselineNotice)
		baselineAttempt := m.anthropicTierAttemptFor(baselineOpts, baselinePrep, baselineNotice).attempt(m.recordFastServed)
		*m.fastServed = baselineOpts.FastMode
		*m.crossFormat = false
		*m.respSummary = translate.ResponseSummary{}
		*m.reqStats = providers.RequestMutationStats{}
		logUpstreamBody(log, routeRes.SessionKey, baselineDecision, feats, baselinePrep.Body)
		m.winnerIdx, m.proxyErr = s.dispatchWithFallback(baselineCtx, dispatch.FailoverInputs{
			Writer:          m.contentSink,
			Prelude:         preludeBuf,
			InitialDecision: baselineDecision,
			Bindings:        baselineBindings,
			Attempt:         baselineAttempt,
			FlushErr:        m.flushErrAsAnthropic,
			Purpose:         routeRes.DispatchPurpose(inference.PurposeAnthropicMessages),
			Origin:          routeRes.RescueOrigin(),
		})
		m.subscriptionPoolFailure = isSubscriptionPoolError(m.proxyErr)
		m.decision = baselineDecision
		m.bindings = baselineBindings
		m.baselineAttempted = true
		m.baselineFailoverUsed = m.proxyErr == nil
	}
	return ctx
}

func (m *messagesRescue) subscriptionRetry(ctx context.Context) {
	s := m.svc
	r := m.r
	log := m.log
	env := m.env
	routeRes := m.routeRes
	opts := m.opts
	preludeBuf := m.preludeBuf
	feats := m.feats
	if m.subscriptionRetryEligible && !paidFallbackForbiddenForModelOnProvider(ctx, m.decision.Provider, m.decision.Model) && !m.baselineAttempted && m.proxyErr != nil &&
		!preludeBuf.Committed() &&
		(providers.IsRetryable(m.proxyErr) || anthropicOAuthCredentialRejected(m.proxyErr) || anthropicSubscriptionModelRejected(m.proxyErr) || anthropicSubscriptionThirdPartyRefused(m.proxyErr)) {
		m.subscriptionRetryRan = true
		recordOwnClaudeLoginRejected(ctx, r.Header, m.usageObserver, m.clock.Now(), m.proxyErr)
		subCtx := subscriptionStatePaidRescueContext(withSuppressedClaudeSubscription(ctx), m.decision.Model)
		subCtx = resolveAndInjectCredentials(subCtx, providers.ProviderAnthropic, m.decision.Model, r.Header)
		subOpts := opts
		subOpts.FastMode = fastModeForAttempt(subCtx, m.decision.Model, providers.ProviderAnthropic)
		subPrep, subEmitErr := env.PrepareAnthropic(r.Header, subOpts)
		if subEmitErr != nil {
			log.Error("Subscription failover: emit Anthropic body failed; surfacing original error", "err", subEmitErr, "model", m.decision.Model)
			if !m.rescueArmed {
				m.flushDeferredErr()
			}
		} else if subBindings := m.routing.ResolveBindingsForDispatch(subCtx, m.decision); len(subBindings) == 0 {
			log.Warn("Subscription failover: no fallback Anthropic binding available; surfacing original error",
				"model", m.decision.Model,
				"err", m.proxyErr,
				"upstream_status", upstreamStatus(m.proxyErr))
			if !m.rescueArmed {
				m.flushDeferredErr()
			}
		} else {
			log.Warn("Subscription failover: subscription rejected the turn, retrying requested model on Weave key",
				"model", m.decision.Model,
				"err", m.proxyErr,
				"upstream_status", upstreamStatus(m.proxyErr))
			subNotice := m.markers.ClaudeNotice(m.markers.WithOwnClaudeRejectionNudge(ctx), subOpts.TargetModel, m.marker)
			subNotice = addRoutingFundingSource(subCtx, m.seats, providers.ProviderAnthropic, subOpts.TargetModel, subNotice)
			subAttempt := m.anthropicTierAttemptFor(subOpts, subPrep, subNotice).attempt(m.recordFastServed)
			*m.fastServed = subOpts.FastMode
			*m.crossFormat = false
			*m.respSummary = translate.ResponseSummary{}
			*m.reqStats = providers.RequestMutationStats{}
			logUpstreamBody(log, routeRes.SessionKey, m.decision, feats, subPrep.Body)
			m.subscriptionFailoverAttempted = true
			subscriptionErr := m.proxyErr
			m.winnerIdx, m.proxyErr = s.dispatchWithFallback(subCtx, dispatch.FailoverInputs{
				Writer:                 m.contentSink,
				Prelude:                preludeBuf,
				InitialDecision:        m.decision,
				Bindings:               subBindings,
				Attempt:                subAttempt,
				FlushErr:               m.flushErrAsAnthropic,
				DeferFlushOnExhaustion: m.rescueArmed,
				Purpose:                routeRes.DispatchPurpose(inference.PurposeAnthropicMessages),
				Origin:                 routeRes.DispatchOrigin(m.decision),
			})
			m.subscriptionPoolFailure = isSubscriptionPoolError(m.proxyErr)
			m.bindings = subBindings
			m.subscriptionFailoverUsed = m.proxyErr == nil
			if credentialRejectionStands(subscriptionErr, m.proxyErr, anthropicOAuthCredentialRejected) {
				m.proxyErr = subscriptionErr
				m.subscriptionPoolFailure = false
				m.flushDeferredErr()
			}
		}
	}
	if m.subscriptionRetryEligible && !m.rescueArmed && !m.baselineAttempted && !m.subscriptionRetryRan && m.proxyErr != nil && !preludeBuf.Committed() {
		m.flushDeferredErr()
	}
}
