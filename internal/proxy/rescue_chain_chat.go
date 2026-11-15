package proxy

import (
	"context"
	"net/http"
	"strings"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

// chatRescue is the OpenAI Chat / Responses rescue chain: the Codex and Claude
// subscription retries and the cyber-refusal retry ahead of the shared
// sibling rescue.
type chatRescue struct {
	*rescueChain
	isResponses         bool
	responsesPreludeBuf *preludeBuffer
	// Shared with the entrypoint's attempt closures, which read or write them too.
	cyberRefusalSeen     *bool
	cyberRetryArmed      *bool
	preOutputRescueArmed *bool

	cyberRetryTarget  router.Decision
	cyberRetryViable  bool
	codexRetryViable  bool
	claudeRetryViable bool

	codexFailoverUsed  bool
	codexRetryRan      bool
	claudeFailoverUsed bool
	claudeRetryRan     bool
	cyberRetryRan      bool
}

func (o *chatRescue) armRescues(ctx context.Context) context.Context {
	ctx = o.armSiblings(ctx, o.routeRes.CallerModelPassthrough || strings.HasPrefix(o.decision.Reason, translate.ReasonUserForceModel))
	*o.preOutputRescueArmed = o.codexRetryViable || o.rescueArmed
	return ctx
}

func (o *chatRescue) dispatchPrimary(ctx context.Context) {
	o.dispatchPrimaryAttempt(ctx, o.cyberRetryViable || o.codexRetryViable || o.claudeRetryViable || o.rescueArmed, nil)
	if o.subscriptionStatePaidTargetAttempted {
		o.codexRetryViable = false
		o.claudeRetryViable = false
	}
	*o.cyberRefusalSeen = *o.cyberRefusalSeen || providers.IsUpstreamCyberPolicyRefusal(o.proxyErr)
}

func (o *chatRescue) armCyberRetry(ctx context.Context) {
	s := o.svc
	routeRes := o.routeRes
	// OpenAI's cybersecurity classifier declines the turn itself, so the rescue
	// has to leave the vendor — a retry on another OpenAI binding meets the same
	// classifier. Resolved pre-dispatch: the refusal gate must be armed before
	// the first upstream byte, and the primary dispatch has to hold its
	// exhaustion flush so the refusal envelope can still be swallowed.
	_, o.explicitRequestedModel = catalog.ByID(o.req.RequestedModel)
	cyberRetryEligible := s.ResolveCyberRefusalRetry(ctx) &&
		!routeRes.CallerModelPassthrough &&
		o.decision.Provider == providers.ProviderOpenAI &&
		!strings.HasPrefix(o.decision.Reason, translate.ReasonUserForceModel) &&
		!s.isHardPinnedTurn(ctx, routeRes.TurnType) &&
		!billing.SubscriptionOnlyFromContext(ctx)
	if cyberRetryEligible {
		target, found := s.cyberRefusalRetryTarget(ctx, o.decision, routeRes.SessionKey, stickyStateRole(routeRes),
			o.overflowEstimate, o.env.SignatureTokenSavings(), o.outputReserve)
		o.cyberRetryViable = found && (s.shouldFailover(ctx) || s.gatewaySiblingAllowed(ctx, target)) &&
			(!subscriptionStateModelsEnabled(ctx) || !paidFallbackForbiddenForModel(ctx, target.Model))
		o.cyberRetryTarget = target
	}
	*o.cyberRetryArmed = o.cyberRetryViable
}

func (o *chatRescue) armSubscriptionRetries(ctx context.Context) {
	s := o.svc
	// Codex-subscription failover: a turn served on the caller's ChatGPT plan is
	// pinned to that one credential, so a plan throttle (429 usage_limit_reached)
	// or a rejected OAuth token has no binding to walk and reaches Codex raw.
	// Retry the same model once on the Weave/BYOK OpenAI key so an exhausted plan
	// rolls over to Weave credits. Suppressed when credits are depleted, where
	// paid spend is exactly what the caller forbade; a linked-first turn's
	// credits are intact, so its throttle rolls over like any other.
	o.codexRetryViable = o.decision.Provider == providers.ProviderOpenAI &&
		servedOnCodexSubscription(ctx) &&
		!paidFallbackForbiddenForModelOn(ctx, o.decision.Provider, o.decision.Model) &&
		s.openaiFallbackKeyAvailable(ctx)
	// OpenAI-compatible callers can route to Anthropic too; give their Claude
	// subscription model-access and third-party refusals the same paid recovery
	// as /v1/messages.
	o.claudeRetryViable = o.decision.Provider == providers.ProviderAnthropic &&
		servedOnSubscription(ctx) &&
		!paidFallbackForbiddenForModelOn(ctx, o.decision.Provider, o.decision.Model) &&
		s.anthropicFallbackKeyAvailable(ctx)
}

// A released prelude can only take an SSE error frame, so every dispatch in
// the chain renders through this — a JSON envelope appended to a live stream
// is unparseable to the client.
func (o *chatRescue) flushErrAsOpenAI(w http.ResponseWriter, err error) {
	// Native Responses bytes are passed through verbatim; there is no
	// error frame we can synthesize into that stream.
	if o.isResponses && o.responsesPreludeBuf != nil && o.responsesPreludeBuf.PreludeSent() {
		return
	}
	if o.env.Stream() && o.preludeBuf.PreludeSent() {
		_ = emitOpenAISSEErrorEvent(w, err)
		return
	}
	// Pre-commit, the handler renders the classified sentinel itself.
	if isSubscriptionPoolError(err) {
		return
	}
	flushUpstreamErrorAsOpenAI(w, err)
}

// The deferred upstream error must reach the client exactly once: the rescue
// owns it, and flushes it itself when it declines to run. A managed-pool
// failure is rendered by the handler from the classified sentinel, so the
// proxy must not write it here as well.
func (o *chatRescue) flushDeferredErr() {
	if o.deferredErrFlushed {
		return
	}
	o.deferredErrFlushed = true
	if o.isResponses && o.responsesPreludeBuf != nil && o.responsesPreludeBuf.PreludeSent() {
		return
	}
	if o.env.Stream() && o.preludeBuf.PreludeSent() {
		// The handler cannot render anything once the client has bytes,
		// so an in-stream frame is the only way the turn reports at all —
		// including for a pool sentinel, which would otherwise truncate
		// the stream silently. Keep the sentinel as the returned error so
		// the handler still classifies and logs it.
		sseErr := emitOpenAISSEErrorEvent(o.contentSink, o.proxyErr)
		if !o.subscriptionPoolFailure {
			o.proxyErr = sseErr
		}
		return
	}
	// Pre-commit, the handler renders the classified sentinel itself.
	if o.subscriptionPoolFailure {
		return
	}
	flushUpstreamErrorAsOpenAI(o.contentSink, o.proxyErr)
}

func (o *chatRescue) codexRetry(ctx context.Context) {
	s := o.svc
	r := o.r
	log := o.log
	routeRes := o.routeRes
	preludeBuf := o.preludeBuf
	if o.codexRetryViable && !paidFallbackForbiddenForModelOn(ctx, o.decision.Provider, o.decision.Model) && o.proxyErr != nil && !preludeBuf.Committed() &&
		(providers.IsRetryable(o.proxyErr) || codexOAuthCredentialRejected(o.proxyErr) || codexSubscriptionModelRejected(o.proxyErr)) {
		// Remember the plan is spent so later turns suppress the token pre-dispatch
		// instead of buying another rejected round-trip per turn until it resets.
		s.recordCodexQuotaExhaustion(ctx, r.Header, o.proxyErr)
		subCtx := subscriptionStatePaidRescueContext(withSuppressedCodexSubscription(ctx), o.decision.Model)
		subCtx = resolveAndInjectCredentials(subCtx, providers.ProviderOpenAI, o.decision.Model, r.Header)
		subOpts := o.opts
		subOpts.FastMode = fastModeForAttempt(subCtx, o.decision.Model, providers.ProviderOpenAI)
		subAttempt, subBuildErr := o.buildAttempt(o.decision, subOpts, o.marker, subCtx)
		subBindings := s.resolveBindingsForDispatch(subCtx, o.decision)
		switch {
		case subBuildErr != nil:
			log.Error("Codex subscription failover: preparing the Weave-key retry failed; surfacing the original error",
				"err", subBuildErr, "model", o.decision.Model)
		case len(subBindings) == 0:
			// No usable OpenAI binding under suppression — surface the real
			// upstream error rather than a synthetic failure that masks it.
			log.Warn("Codex subscription failover: no fallback OpenAI binding available; surfacing the original error",
				"model", o.decision.Model, "upstream_status", upstreamStatus(o.proxyErr))
		default:
			log.Warn("Codex subscription failover: ChatGPT plan rejected the turn, retrying on Weave credits",
				"model", o.decision.Model,
				"err", o.proxyErr,
				"upstream_status", upstreamStatus(o.proxyErr))
			o.codexRetryRan = true
			*o.respSummary = translate.ResponseSummary{}
			subscriptionErr := o.proxyErr
			o.winnerIdx, o.proxyErr = s.dispatchWithFallback(subCtx, failoverInputs{
				w:               o.contentSink,
				buf:             preludeBuf,
				initialDecision: o.decision,
				bindings:        subBindings,
				attempt:         subAttempt,
				flushErr:        o.flushErrAsOpenAI,
				// A failed retry keeps the same model; hold the error so the
				// cyber-refusal rescue below can still serve the turn.
				deferFlushOnExhaustion: o.cyberRetryViable || o.rescueArmed,
				purpose:                routeRes.dispatchPurpose(o.surface),
				origin:                 routeRes.dispatchOrigin(o.decision),
			})
			o.bindings = subBindings
			o.codexFailoverUsed = o.proxyErr == nil
			o.subscriptionPoolFailure = isSubscriptionPoolError(o.proxyErr)
			*o.cyberRefusalSeen = *o.cyberRefusalSeen || providers.IsUpstreamCyberPolicyRefusal(o.proxyErr)
			if credentialRejectionStands(subscriptionErr, o.proxyErr, codexOAuthCredentialRejected) {
				o.proxyErr = subscriptionErr
				o.subscriptionPoolFailure = false
				o.flushDeferredErr()
			}
		}
	}
}

func (o *chatRescue) claudeRetry(ctx context.Context) {
	s := o.svc
	log := o.log
	routeRes := o.routeRes
	preludeBuf := o.preludeBuf
	if o.claudeRetryViable && !paidFallbackForbiddenForModelOn(ctx, o.decision.Provider, o.decision.Model) && o.proxyErr != nil && !preludeBuf.Committed() && claudeRetryRefusal(ctx, o.proxyErr) {
		subCtx := subscriptionStatePaidRescueContext(withSuppressedClaudeSubscription(ctx), o.decision.Model)
		subCtx = resolveAndInjectCredentials(subCtx, providers.ProviderAnthropic, o.decision.Model, o.r.Header)
		subOpts := o.opts
		subOpts.FastMode = fastModeForAttempt(subCtx, o.decision.Model, providers.ProviderAnthropic)
		subAttempt, subBuildErr := o.buildAttempt(o.decision, subOpts, o.marker, subCtx)
		subBindings := s.resolveBindingsForDispatch(subCtx, o.decision)
		switch {
		case subBuildErr != nil:
			log.Error("Claude subscription failover: preparing the Weave-key retry failed; surfacing the original error",
				"err", subBuildErr, "model", o.decision.Model)
		case len(subBindings) == 0:
			log.Warn("Claude subscription failover: no fallback Anthropic binding available; surfacing the original error",
				"model", o.decision.Model, "upstream_status", upstreamStatus(o.proxyErr))
		default:
			log.Warn("Claude subscription failover: subscription rejected the turn, retrying on Weave credits",
				"model", o.decision.Model,
				"err", o.proxyErr,
				"upstream_status", upstreamStatus(o.proxyErr))
			o.claudeRetryRan = true
			*o.respSummary = translate.ResponseSummary{}
			o.winnerIdx, o.proxyErr = s.dispatchWithFallback(subCtx, failoverInputs{
				w:                      o.contentSink,
				buf:                    preludeBuf,
				initialDecision:        o.decision,
				bindings:               subBindings,
				attempt:                subAttempt,
				flushErr:               o.flushErrAsOpenAI,
				deferFlushOnExhaustion: o.cyberRetryViable || o.rescueArmed,
				purpose:                routeRes.dispatchPurpose(o.surface),
				origin:                 routeRes.dispatchOrigin(o.decision),
			})
			o.bindings = subBindings
			o.claudeFailoverUsed = o.proxyErr == nil
			o.subscriptionPoolFailure = isSubscriptionPoolError(o.proxyErr)
			*o.cyberRefusalSeen = *o.cyberRefusalSeen || providers.IsUpstreamCyberPolicyRefusal(o.proxyErr)
		}
	}

	// A subscription retry declined to run and no other rescue owns the held
	// error; surface it now so it is never dropped.
	if ((o.codexRetryViable && !o.codexRetryRan) || (o.claudeRetryViable && !o.claudeRetryRan)) &&
		!o.cyberRetryViable && !o.rescueArmed && o.proxyErr != nil && !preludeBuf.Committed() {
		o.flushDeferredErr()
	}
}

func (o *chatRescue) cyberRetry(ctx context.Context) {
	s := o.svc
	r := o.r
	log := o.log
	routeRes := o.routeRes
	preludeBuf := o.preludeBuf
	if o.cyberRetryViable && o.proxyErr != nil && !preludeBuf.Committed() &&
		providers.IsUpstreamCyberPolicyRefusal(o.proxyErr) {
		retryOpts, retryEffort := switchedAttemptOptions(ctx, o.opts, o.cyberRetryTarget, routeRes.EscalateEffort, s.resolveEffort)
		retryCtx := resolveAndInjectCredentials(subscriptionStatePaidRescueContext(ctx, o.cyberRetryTarget.Model), o.cyberRetryTarget.Provider, o.cyberRetryTarget.Model, r.Header)
		retryOpts.FastMode = fastModeForAttempt(retryCtx, o.cyberRetryTarget.Model, o.cyberRetryTarget.Provider)
		retryBindings := s.resolveBindingsForDispatch(retryCtx, o.cyberRetryTarget)
		retryMarker := withRescueDetail(suppressMarkerIfRequested(ctx, r.Header, modelSelectionMarkerForRequest(ctx, routeRes, cyberRefusalRoutingMarkerFor(routeRes, o.cyberRetryTarget.Model), o.cyberRetryTarget.Model, markerReasonCyberRefusal)), lastFailureDetail(attemptTraceFromContext(ctx)))
		retryAttempt, retryBuildErr := o.buildAttempt(o.cyberRetryTarget, retryOpts, retryMarker, retryCtx)
		switch {
		case retryBuildErr != nil:
			log.Error("Cyber-refusal retry: preparing the fallback request failed; surfacing the refusal",
				"err", retryBuildErr,
				"fallback_model", o.cyberRetryTarget.Model)
		case len(retryBindings) == 0:
			log.Warn("Cyber-refusal retry: fallback has no usable binding; surfacing the refusal",
				"fallback_model", o.cyberRetryTarget.Model,
				"fallback_provider", o.cyberRetryTarget.Provider)
		default:
			log.Warn("Cyber-refusal retry: OpenAI declined the turn on cyber policy, retrying off-vendor",
				"failed_model", o.primaryModel,
				"failed_provider", o.primaryProvider,
				"fallback_model", o.cyberRetryTarget.Model,
				"fallback_provider", o.cyberRetryTarget.Provider)
			o.cyberRetryRan = true
			o.effortServed = retryEffort
			*o.respSummary = translate.ResponseSummary{}
			o.winnerIdx, o.proxyErr = s.dispatchWithFallback(retryCtx, failoverInputs{
				w:                      o.contentSink,
				buf:                    preludeBuf,
				initialDecision:        o.cyberRetryTarget,
				bindings:               retryBindings,
				attempt:                retryAttempt,
				flushErr:               o.flushErrAsOpenAI,
				deferFlushOnExhaustion: o.rescueArmed,
				purpose:                routeRes.dispatchPurpose(o.surface),
				origin:                 routeRes.rescueOrigin(),
			})
			o.subscriptionPoolFailure = isSubscriptionPoolError(o.proxyErr)
			o.decision = o.cyberRetryTarget
			o.bindings = retryBindings
			o.marker = retryMarker
		}
	}
	// The rescue declined to run; surface the held error now so it's never dropped.
	if o.cyberRetryViable && !o.cyberRetryRan && !o.rescueArmed && o.proxyErr != nil && !preludeBuf.Committed() {
		o.flushDeferredErr()
	}
}

func (o *chatRescue) finish(ctx context.Context) {
	// No rescuer served (none ran, or the last one failed pre-commit); surface
	// the held error now so it's never dropped.
	o.proxyErr = asCompactionUnservable(o.routeRes.TurnType, o.decision, o.proxyErr)
	if (o.siblingRescueOwed || o.overflowRescueOnly) && o.proxyErr != nil && !o.preludeBuf.Committed() {
		o.flushDeferredErr()
	}

	if subscriptionCredentialFallbackUsed(ctx) {
		switch o.decision.Provider {
		case providers.ProviderOpenAI:
			o.codexFailoverUsed = true
		case providers.ProviderAnthropic:
			o.claudeFailoverUsed = true
		}
	}
	o.finalProvider = o.servedProvider(o.decision.Provider)
	o.decision.Provider = o.finalProvider
	o.failoverUsed = o.finalProvider != o.primaryProvider || o.codexFailoverUsed || o.claudeFailoverUsed || o.siblingFailoverUsed
	o.failoverAttempted = o.failoverUsed || o.codexRetryRan || o.claudeRetryRan || o.siblingRescueRan
}
