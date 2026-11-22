package proxy

import (
	"context"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

func (m *messagesRescue) baselineRescue(ctx context.Context) context.Context {
	preludeBuf := m.PreludeBuf
	if m.ProxyErr != nil && !preludeBuf.Committed() &&
		((m.baselineEligible && (providers.IsRetryable(m.ProxyErr) || providers.IsUpstreamModelNotFound(m.ProxyErr))) ||
			(m.baselineViable && (m.capabilityRejected || translate.IsIntrinsicallyIncompatible(m.ProxyErr) || m.CrossBindingRejected))) {
		ctx = m.dispatchBaseline(ctx)
	} else if m.baselineViable && !m.RescueArmed && m.ProxyErr != nil {
		m.flushDeferredErr()
	}
	return ctx
}

func (m *messagesRescue) dispatchBaseline(ctx context.Context) context.Context {
	s := m.svc
	r := m.HTTPRequest
	log := m.Log
	env := m.Env
	routeRes := m.RouteRes
	opts := m.Opts
	preludeBuf := m.PreludeBuf
	feats := m.feats
	baselineDecision := m.Decision
	baselineDecision.Model = m.baselineModel
	baselineDecision.Provider = providers.ProviderAnthropic
	baselineDecision.Effort = ""
	baselineOpts, baselineEffort := switchedAttemptOptions(ctx, opts, baselineDecision, routeRes.EscalateEffort, s.resolveEffort)
	baselineOpts.ModelSwitched = baseModelOf(routeRes.PriorServedModel) != m.baselineModel ||
		routeRes.SessionEverSwitched
	baselineCtx := subscriptionStatePaidRescueContext(ctx, m.baselineModel)
	baselineSubExhausted := m.Credentials.ClaudeSubscriptionExhausted(ctx, r.Header)
	if baselineSubExhausted {
		baselineCtx = credentials.WithSuppressedClaudeSubscription(baselineCtx)
	}
	baselineCtx = m.Credentials.ResolveCredentials(baselineCtx, providers.ProviderAnthropic, m.baselineModel, r.Header)
	baselineOpts.FastMode = fastModeForAttempt(baselineCtx, m.baselineModel, providers.ProviderAnthropic)
	baselinePrep, baselineEmitErr := env.PrepareAnthropic(r.Header, baselineOpts)
	if baselineEmitErr != nil {
		log.Error("Baseline failover: emit Anthropic body failed; surfacing original error", "err", baselineEmitErr, "baseline_model", m.baselineModel)
		if !m.RescueArmed {
			m.flushDeferredErr()
		}
	} else {
		log.Warn("Baseline failover: retrying requested model on Anthropic",
			"failed_model", m.Decision.Model,
			"failed_provider", m.PrimaryProvider,
			"baseline_model", m.baselineModel,
			"err", m.ProxyErr)
		if baselineSubExhausted {
			ctx = credentials.WithSuppressedClaudeSubscription(ctx)
		}
		m.EffortServed = baselineEffort
		baselineBindings := m.Routing.ResolveBindingsForDispatch(baselineCtx, baselineDecision)
		baselineMarker := m.Markers.WithFailureDetail(ctx, m.Markers.Baseline(ctx, m.baselineModel))
		baselineNotice := m.Markers.ClaudeNotice(ctx, baselineOpts.TargetModel, baselineMarker)
		baselineNotice = addRoutingFundingSource(baselineCtx, m.seats, providers.ProviderAnthropic, baselineOpts.TargetModel, baselineNotice)
		baselineAttempt := m.anthropicTierAttemptFor(baselineOpts, baselinePrep, baselineNotice).attempt(m.recordFastServed)
		*m.fastServed = baselineOpts.FastMode
		*m.crossFormat = false
		*m.RespSummary = translate.ResponseSummary{}
		*m.ReqStats = providers.RequestMutationStats{}
		logUpstreamBody(log, routeRes.SessionKey, baselineDecision, feats, baselinePrep.Body)
		m.WinnerIdx, m.ProxyErr = s.dispatchWithFallback(baselineCtx, dispatch.FailoverInputs{
			Writer:          m.ContentSink,
			Prelude:         preludeBuf,
			InitialDecision: baselineDecision,
			Bindings:        baselineBindings,
			Attempt:         baselineAttempt,
			FlushErr:        m.flushErrAsAnthropic,
			Purpose:         routeRes.DispatchPurpose(inference.PurposeAnthropicMessages),
			Origin:          routeRes.RescueOrigin(),
		})
		m.SubscriptionPoolFailure = isSubscriptionPoolError(m.ProxyErr)
		m.Decision = baselineDecision
		m.Bindings = baselineBindings
		m.BaselineAttempted = true
		m.baselineFailoverUsed = m.ProxyErr == nil
	}
	return ctx
}

func (m *messagesRescue) subscriptionRetry(ctx context.Context) {
	s := m.svc
	r := m.HTTPRequest
	log := m.Log
	env := m.Env
	routeRes := m.RouteRes
	opts := m.Opts
	preludeBuf := m.PreludeBuf
	feats := m.feats
	if m.subscriptionRetryEligible && !paidFallbackForbiddenForModelOnProvider(ctx, m.Decision.Provider, m.Decision.Model) && !m.BaselineAttempted && m.ProxyErr != nil &&
		!preludeBuf.Committed() &&
		(providers.IsRetryable(m.ProxyErr) || anthropicOAuthCredentialRejected(m.ProxyErr) || anthropicSubscriptionModelRejected(m.ProxyErr) || anthropicSubscriptionThirdPartyRefused(m.ProxyErr)) {
		m.subscriptionRetryRan = true
		recordOwnClaudeLoginRejected(ctx, r.Header, m.usageObserver, m.clock.Now(), m.ProxyErr)
		subCtx := subscriptionStatePaidRescueContext(credentials.WithSuppressedClaudeSubscription(ctx), m.Decision.Model)
		subCtx = resolveAndInjectCredentials(subCtx, providers.ProviderAnthropic, m.Decision.Model, r.Header)
		subOpts := opts
		subOpts.FastMode = fastModeForAttempt(subCtx, m.Decision.Model, providers.ProviderAnthropic)
		subPrep, subEmitErr := env.PrepareAnthropic(r.Header, subOpts)
		if subEmitErr != nil {
			log.Error("Subscription failover: emit Anthropic body failed; surfacing original error", "err", subEmitErr, "model", m.Decision.Model)
			if !m.RescueArmed {
				m.flushDeferredErr()
			}
		} else if subBindings := m.Routing.ResolveBindingsForDispatch(subCtx, m.Decision); len(subBindings) == 0 {
			log.Warn("Subscription failover: no fallback Anthropic binding available; surfacing original error",
				"model", m.Decision.Model,
				"err", m.ProxyErr,
				"upstream_status", upstreamStatus(m.ProxyErr))
			if !m.RescueArmed {
				m.flushDeferredErr()
			}
		} else {
			log.Warn("Subscription failover: subscription rejected the turn, retrying requested model on Weave key",
				"model", m.Decision.Model,
				"err", m.ProxyErr,
				"upstream_status", upstreamStatus(m.ProxyErr))
			subNotice := m.Markers.ClaudeNotice(m.Markers.WithOwnClaudeRejectionNudge(ctx), subOpts.TargetModel, m.Marker)
			subNotice = addRoutingFundingSource(subCtx, m.seats, providers.ProviderAnthropic, subOpts.TargetModel, subNotice)
			subAttempt := m.anthropicTierAttemptFor(subOpts, subPrep, subNotice).attempt(m.recordFastServed)
			*m.fastServed = subOpts.FastMode
			*m.crossFormat = false
			*m.RespSummary = translate.ResponseSummary{}
			*m.ReqStats = providers.RequestMutationStats{}
			logUpstreamBody(log, routeRes.SessionKey, m.Decision, feats, subPrep.Body)
			m.subscriptionFailoverAttempted = true
			subscriptionErr := m.ProxyErr
			m.WinnerIdx, m.ProxyErr = s.dispatchWithFallback(subCtx, dispatch.FailoverInputs{
				Writer:                 m.ContentSink,
				Prelude:                preludeBuf,
				InitialDecision:        m.Decision,
				Bindings:               subBindings,
				Attempt:                subAttempt,
				FlushErr:               m.flushErrAsAnthropic,
				DeferFlushOnExhaustion: m.RescueArmed,
				Purpose:                routeRes.DispatchPurpose(inference.PurposeAnthropicMessages),
				Origin:                 routeRes.DispatchOrigin(m.Decision),
			})
			m.SubscriptionPoolFailure = isSubscriptionPoolError(m.ProxyErr)
			m.Bindings = subBindings
			m.subscriptionFailoverUsed = m.ProxyErr == nil
			if credentialRejectionStands(subscriptionErr, m.ProxyErr, anthropicOAuthCredentialRejected) {
				m.ProxyErr = subscriptionErr
				m.SubscriptionPoolFailure = false
				m.flushDeferredErr()
			}
		}
	}
	if m.subscriptionRetryEligible && !m.RescueArmed && !m.BaselineAttempted && !m.subscriptionRetryRan && m.ProxyErr != nil && !preludeBuf.Committed() {
		m.flushDeferredErr()
	}
}
