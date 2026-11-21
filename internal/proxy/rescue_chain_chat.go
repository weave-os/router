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

type chatRescue struct {
	*rescueChain
	isResponses          bool
	responsesPreludeBuf  *preludeBuffer
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

func (o *chatRescue) armCyberRetry(ctx context.Context, strategy router.Strategy) {
	routeRes := o.routeRes
	_, o.explicitRequestedModel = catalog.ByID(o.req.RequestedModel)
	cyberRetryEligible := o.routing.ResolveCyberRefusalRetry(ctx) &&
		!routeRes.CallerModelPassthrough &&
		o.decision.Provider == providers.ProviderOpenAI &&
		!strings.HasPrefix(o.decision.Reason, translate.ReasonUserForceModel) &&
		!o.routing.IsHardPinnedTurn(ctx, strategy, routeRes.TurnType) &&
		!billing.SubscriptionOnlyFromContext(ctx)
	if cyberRetryEligible {
		target, found := o.routing.CyberRefusalRetryTarget(ctx, o.decision, strategy, routeRes.InstallationID, routeRes.SessionKey, stickyStateRole(routeRes),
			o.overflowEstimate, o.env.SignatureTokenSavings(), o.outputReserve)
		o.cyberRetryViable = found && (o.routing.ShouldFailover(ctx) || o.routing.GatewaySiblingAllowed(ctx, target)) &&
			(!subscriptionStateModelsEnabled(ctx) || !paidFallbackForbiddenForModel(ctx, target.Model))
		o.cyberRetryTarget = target
	}
	*o.cyberRetryArmed = o.cyberRetryViable
}

func (o *chatRescue) armSubscriptionRetries(ctx context.Context) {
	o.codexRetryViable = o.decision.Provider == providers.ProviderOpenAI &&
		servedOnCodexSubscription(ctx) &&
		!paidFallbackForbiddenForModel(ctx, o.decision.Model) &&
		o.credentials.OpenAIFallbackKeyAvailable(ctx)
	o.claudeRetryViable = o.decision.Provider == providers.ProviderAnthropic &&
		servedOnSubscription(ctx) &&
		!paidFallbackForbiddenForModel(ctx, o.decision.Model) &&
		o.credentials.AnthropicFallbackKeyAvailable(ctx)
}

func (o *chatRescue) flushErrAsOpenAI(w http.ResponseWriter, err error) {
	if o.isResponses && o.responsesPreludeBuf != nil && o.responsesPreludeBuf.PreludeSent() {
		return
	}
	if o.env.Stream() && o.preludeBuf.PreludeSent() {
		_ = emitOpenAISSEErrorEvent(w, err)
		return
	}
	if isSubscriptionPoolError(err) {
		return
	}
	flushUpstreamErrorAsOpenAI(w, err)
}

func (o *chatRescue) flushDeferredErr() {
	if o.deferredErrFlushed {
		return
	}
	o.deferredErrFlushed = true
	if o.isResponses && o.responsesPreludeBuf != nil && o.responsesPreludeBuf.PreludeSent() {
		return
	}
	if o.env.Stream() && o.preludeBuf.PreludeSent() {
		sseErr := emitOpenAISSEErrorEvent(o.contentSink, o.proxyErr)
		if !o.subscriptionPoolFailure {
			o.proxyErr = sseErr
		}
		return
	}
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
	if o.codexRetryViable && !paidFallbackForbiddenForModel(ctx, o.decision.Model) && o.proxyErr != nil && !preludeBuf.Committed() &&
		(providers.IsRetryable(o.proxyErr) || codexOAuthCredentialRejected(o.proxyErr) || codexSubscriptionModelRejected(o.proxyErr)) {
		o.credentials.RecordCodexQuotaExhaustion(ctx, r.Header, o.proxyErr)
		subCtx := subscriptionStatePaidRescueContext(withSuppressedCodexSubscription(ctx), o.decision.Model)
		subCtx = resolveAndInjectCredentials(subCtx, providers.ProviderOpenAI, o.decision.Model, r.Header)
		subOpts, subEffort := o.opts, o.effortServed
		if subOpts.TargetModel != o.decision.Model {
			subOpts, subEffort = switchedAttemptOptions(ctx, o.opts, o.decision, routeRes.EscalateEffort, s.resolveEffort)
		}
		subOpts.FastMode = fastModeForAttempt(subCtx, o.decision.Model, providers.ProviderOpenAI)
		subAttempt, subBuildErr := o.buildAttempt(o.decision, subOpts, o.marker, subCtx)
		subBindings := o.routing.ResolveBindingsForDispatch(subCtx, o.decision)
		switch {
		case subBuildErr != nil:
			log.Error("Codex subscription failover: preparing the Weave-key retry failed; surfacing the original error",
				"err", subBuildErr, "model", o.decision.Model)
		case len(subBindings) == 0:
			log.Warn("Codex subscription failover: no fallback OpenAI binding available; surfacing the original error",
				"model", o.decision.Model, "upstream_status", upstreamStatus(o.proxyErr))
		default:
			log.Warn("Codex subscription failover: ChatGPT plan rejected the turn, retrying on Weave credits",
				"model", o.decision.Model,
				"err", o.proxyErr,
				"upstream_status", upstreamStatus(o.proxyErr))
			o.codexRetryRan = true
			o.effortServed = subEffort
			*o.respSummary = translate.ResponseSummary{}
			subscriptionErr := o.proxyErr
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
	if o.claudeRetryViable && !paidFallbackForbiddenForModel(ctx, o.decision.Model) && o.proxyErr != nil && !preludeBuf.Committed() && (anthropicSubscriptionModelRejected(o.proxyErr) || anthropicSubscriptionThirdPartyRefused(o.proxyErr)) {
		subCtx := subscriptionStatePaidRescueContext(withSuppressedClaudeSubscription(ctx), o.decision.Model)
		subCtx = resolveAndInjectCredentials(subCtx, providers.ProviderAnthropic, o.decision.Model, o.r.Header)
		subOpts := o.opts
		subOpts.FastMode = fastModeForAttempt(subCtx, o.decision.Model, providers.ProviderAnthropic)
		subAttempt, subBuildErr := o.buildAttempt(o.decision, subOpts, o.marker, subCtx)
		subBindings := o.routing.ResolveBindingsForDispatch(subCtx, o.decision)
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
		retryBindings := o.routing.ResolveBindingsForDispatch(retryCtx, o.cyberRetryTarget)
		retryMarker := withRescueDetail(suppressMarkerIfRequested(ctx, r.Header, modelSelectionMarkerForRequest(o.strategyPorts, ctx, routeRes, cyberRefusalRoutingMarkerFor(routeRes, o.cyberRetryTarget.Model), o.cyberRetryTarget.Model, markerReasonCyberRefusal)), lastFailureDetail(attemptTraceFromContext(ctx)))
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
	if o.cyberRetryViable && !o.cyberRetryRan && !o.rescueArmed && o.proxyErr != nil && !preludeBuf.Committed() {
		o.flushDeferredErr()
	}
}

func (o *chatRescue) finish(ctx context.Context) {
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
