package proxy

import (
	"context"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

// baselineRescue handles a routed model whose bindings all failed with a fault
// another model could satisfy, pre-commit: it re-dispatches the requested
// model on Anthropic. crossFormat/respSummary/reqStats reset to
// Anthropic-native values so telemetry reflects the binding that actually
// served.
func (m *messagesRescue) baselineRescue(ctx context.Context) context.Context {
	preludeBuf := m.preludeBuf
	if m.proxyErr != nil && !preludeBuf.Committed() &&
		((m.baselineEligible && (providers.IsRetryable(m.proxyErr) || providers.IsUpstreamModelNotFound(m.proxyErr))) ||
			(m.baselineViable && (m.capabilityRejected || translate.IsIntrinsicallyIncompatible(m.proxyErr) || m.crossBindingRejected))) {
		ctx = m.dispatchBaseline(ctx)
	} else if m.baselineViable && !m.rescueArmed && m.proxyErr != nil {
		// Baseline didn't run (mid-stream commit, or non-failoverable error);
		// surface the deferred original error now. Guard must match
		// deferFlushOnExhaustion above, or a deferred error is never flushed —
		// unless the sibling rescue below owns the deferred flush instead.
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
	// The failed arm's effort does not describe the baseline model.
	baselineDecision.Effort = ""
	baselineOpts, baselineEffort := switchedAttemptOptions(ctx, opts, baselineDecision, routeRes.EscalateEffort, s.resolveEffort)
	// Recompute against the model that actually serves, not the cost-routed
	// OSS id — otherwise PrepareAnthropic may leave stale signed thinking
	// blocks the baseline model rejects (400). Compare bare model IDs:
	// baselineModel carries no effort, and any effort on the prior identity
	// belonged to a different model, so the model comparison already
	// subsumes it.
	baselineOpts.ModelSwitched = baseModelOf(routeRes.PriorServedModel) != m.baselineModel ||
		routeRes.SessionEverSwitched
	baselineCtx := subscriptionStatePaidRescueContext(ctx, m.baselineModel)
	baselineSubExhausted := s.claudeSubscriptionExhausted(ctx, r.Header)
	if baselineSubExhausted {
		baselineCtx = withSuppressedClaudeSubscription(baselineCtx)
	}
	baselineCtx = s.resolveCredentials(baselineCtx, providers.ProviderAnthropic, m.baselineModel, r.Header)
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
		baselineBindings := s.resolveBindingsForDispatch(baselineCtx, baselineDecision)
		baselineMarker := withRescueDetail(suppressMarkerIfRequested(ctx, r.Header, modelSelectionMarkerForRequest(ctx, routeRes, baselineRoutingMarkerFor(routeRes, m.baselineModel), m.baselineModel, markerReasonBaseline)), lastFailureDetail(attemptTraceFromContext(ctx)))
		baselineNotice := routerNoticeForRequest(ctx, r.Header, subscriptions.ProviderClaude, baselineOpts.TargetModel, baselineMarker)
		baselineNotice = addRoutingFundingSource(baselineCtx, providers.ProviderAnthropic, baselineOpts.TargetModel, baselineNotice)
		baselineAttempt := m.anthropicTierAttemptFor(baselineOpts, baselinePrep, baselineNotice).attempt(m.recordFastServed)
		*m.fastServed = baselineOpts.FastMode
		*m.crossFormat = false
		*m.respSummary = translate.ResponseSummary{}
		*m.reqStats = providers.RequestMutationStats{}
		logUpstreamBody(log, routeRes.SessionKey, baselineDecision, feats, baselinePrep.Body)
		m.winnerIdx, m.proxyErr = s.dispatchWithFallback(baselineCtx, failoverInputs{
			w:               m.contentSink,
			buf:             preludeBuf,
			initialDecision: baselineDecision,
			bindings:        baselineBindings,
			attempt:         baselineAttempt,
			flushErr:        m.flushErrAsAnthropic,
			purpose:         routeRes.dispatchPurpose(inference.PurposeAnthropicMessages),
			origin:          routeRes.rescueOrigin(),
		})
		m.subscriptionPoolFailure = isSubscriptionPoolError(m.proxyErr)
		m.decision = baselineDecision
		m.bindings = baselineBindings
		m.baselineAttempted = true
		// Reflect whether the baseline actually served — a failed retry must
		// not report baseline_failover=true and skew bake-off analysis.
		m.baselineFailoverUsed = m.proxyErr == nil
	}
	return ctx
}

// subscriptionRetry is subscription-credit failover: suppress the OAuth token
// and retry the SAME model once on the Weave/BYOK key when a
// subscription-served Anthropic turn hit a transient fault (429/timeout), an
// OAuth rejection (401/403), a model-access 404 (subscription token cannot use
// that Claude model), or a third-party-client refusal (400: plan limits serve
// only Claude Code), pre-commit. Skipped when baseline failover already ran
// (non-Anthropic).
func (m *messagesRescue) subscriptionRetry(ctx context.Context) {
	s := m.svc
	r := m.r
	log := m.log
	env := m.env
	routeRes := m.routeRes
	opts := m.opts
	preludeBuf := m.preludeBuf
	feats := m.feats
	if m.subscriptionRetryEligible && !paidFallbackForbiddenForModelOn(ctx, m.decision.Provider, m.decision.Model) && !m.baselineAttempted && m.proxyErr != nil &&
		!preludeBuf.Committed() &&
		(providers.IsRetryable(m.proxyErr) || anthropicOAuthCredentialRejected(m.proxyErr) || anthropicSubscriptionModelRejected(m.proxyErr) || anthropicSubscriptionThirdPartyRefused(m.proxyErr)) {
		m.subscriptionRetryRan = true
		subCtx := subscriptionStatePaidRescueContext(withSuppressedClaudeSubscription(ctx), m.decision.Model)
		subCtx = resolveAndInjectCredentials(subCtx, providers.ProviderAnthropic, m.decision.Model, r.Header)
		// Model is unchanged, but rebuild prep so the retry gets a pristine
		// PreparedRequest under the suppressed-subscription context — which
		// now pays on the Weave key, so the fast-mode opt-in applies.
		subOpts := opts
		subOpts.FastMode = fastModeForAttempt(subCtx, m.decision.Model, providers.ProviderAnthropic)
		subPrep, subEmitErr := env.PrepareAnthropic(r.Header, subOpts)
		if subEmitErr != nil {
			log.Error("Subscription failover: emit Anthropic body failed; surfacing original error", "err", subEmitErr, "model", m.decision.Model)
			if !m.rescueArmed {
				m.flushDeferredErr()
			}
		} else if subBindings := s.resolveBindingsForDispatch(subCtx, m.decision); len(subBindings) == 0 {
			// No usable Anthropic binding under suppression — surface the
			// original retryable error (real throttle) rather than a synthetic
			// 502 that would mask it. No Weave key attempted, so attribution
			// stays on the subscription.
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
			subNotice := routerNoticeForRequest(ctx, r.Header, subscriptions.ProviderClaude, subOpts.TargetModel, m.marker)
			subNotice = addRoutingFundingSource(subCtx, providers.ProviderAnthropic, subOpts.TargetModel, subNotice)
			subAttempt := m.anthropicTierAttemptFor(subOpts, subPrep, subNotice).attempt(m.recordFastServed)
			*m.fastServed = subOpts.FastMode
			*m.crossFormat = false
			*m.respSummary = translate.ResponseSummary{}
			*m.reqStats = providers.RequestMutationStats{}
			logUpstreamBody(log, routeRes.SessionKey, m.decision, feats, subPrep.Body)
			m.subscriptionFailoverAttempted = true
			subscriptionErr := m.proxyErr
			m.winnerIdx, m.proxyErr = s.dispatchWithFallback(subCtx, failoverInputs{
				w:               m.contentSink,
				buf:             preludeBuf,
				initialDecision: m.decision,
				bindings:        subBindings,
				attempt:         subAttempt,
				flushErr:        m.flushErrAsAnthropic,
				// A failed retry keeps the same dark model; hold the error so
				// the sibling rescue below can still serve the turn.
				deferFlushOnExhaustion: m.rescueArmed,
				purpose:                routeRes.dispatchPurpose(inference.PurposeAnthropicMessages),
				origin:                 routeRes.dispatchOrigin(m.decision),
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
	// The subscription retry didn't run (mid-stream commit, or non-retryable
	// error); surface the deferred original error now so it's never dropped.
	if m.subscriptionRetryEligible && !m.rescueArmed && !m.baselineAttempted && !m.subscriptionRetryRan && m.proxyErr != nil && !preludeBuf.Committed() {
		m.flushDeferredErr()
	}
}
