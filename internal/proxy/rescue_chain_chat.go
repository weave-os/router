package proxy

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

type chatRescue struct {
	*dispatch.RescueChain
	svc                  rescueServices
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
	ctx = o.ArmSiblings(ctx, o.RouteRes.CallerModelPassthrough || strings.HasPrefix(o.Decision.Reason, translate.ReasonUserForceModel))
	*o.preOutputRescueArmed = o.codexRetryViable || o.RescueArmed
	return ctx
}

func (o *chatRescue) dispatchPrimary(ctx context.Context) {
	o.DispatchPrimaryAttempt(ctx, o.cyberRetryViable || o.codexRetryViable || o.claudeRetryViable || o.RescueArmed, nil)
	if o.SubscriptionStatePaidTargetAttempted {
		o.codexRetryViable = false
		o.claudeRetryViable = false
	}
	*o.cyberRefusalSeen = *o.cyberRefusalSeen || providers.IsUpstreamCyberPolicyRefusal(o.ProxyErr)
}

func (o *chatRescue) armCyberRetry(ctx context.Context, strategy router.Strategy) {
	routeRes := o.RouteRes
	_, o.ExplicitRequestedModel = catalog.ByID(o.Request.RequestedModel)
	cyberRetryEligible := o.Routing.ResolveCyberRefusalRetry(ctx) &&
		!routeRes.CallerModelPassthrough &&
		o.Decision.Provider == providers.ProviderOpenAI &&
		!strings.HasPrefix(o.Decision.Reason, translate.ReasonUserForceModel) &&
		!o.Routing.IsHardPinnedTurn(ctx, strategy, routeRes.TurnType) &&
		!billing.SubscriptionOnlyFromContext(ctx)
	if cyberRetryEligible {
		target, found := o.Routing.CyberRefusalRetryTarget(ctx, o.Decision, dispatch.RescueSessionTarget{Strategy: strategy, InstallationID: routeRes.InstallationID, SessionKey: routeRes.SessionKey}, routeRes.StickyRole,
			o.OverflowEstimate, o.Env.SignatureTokenSavings(), o.OutputReserve)
		o.cyberRetryViable = found && (o.Routing.ShouldFailover(ctx) || o.Routing.GatewaySiblingAllowed(ctx, target)) &&
			(!subscriptionStateModelsEnabled(ctx) || !paidFallbackForbiddenForModel(ctx, target.Model))
		o.cyberRetryTarget = target
	}
	*o.cyberRetryArmed = o.cyberRetryViable
}

func (o *chatRescue) armSubscriptionRetries(ctx context.Context) {
	o.codexRetryViable = o.Decision.Provider == providers.ProviderOpenAI &&
		servedOnCodexSubscription(ctx) &&
		!paidFallbackForbiddenForModelOnProvider(ctx, o.Decision.Provider, o.Decision.Model) &&
		o.Credentials.OpenAIFallbackKeyAvailable(ctx)
	o.claudeRetryViable = o.Decision.Provider == providers.ProviderAnthropic &&
		servedOnSubscription(ctx) &&
		!paidFallbackForbiddenForModelOnProvider(ctx, o.Decision.Provider, o.Decision.Model) &&
		o.Credentials.AnthropicFallbackKeyAvailable(ctx)
}

func (o *chatRescue) flushErrAsOpenAI(w http.ResponseWriter, err error) {
	if o.isResponses && o.responsesPreludeBuf != nil && o.responsesPreludeBuf.PreludeSent() {
		return
	}
	if o.Env.Stream() && o.PreludeBuf.PreludeSent() {
		_ = emitOpenAISSEErrorEvent(w, err)
		return
	}
	if isSubscriptionPoolError(err) {
		return
	}
	flushUpstreamErrorAsOpenAI(w, err)
}

func (o *chatRescue) flushDeferredErr() {
	if o.DeferredErrFlushed {
		return
	}
	o.DeferredErrFlushed = true
	if o.isResponses && o.responsesPreludeBuf != nil && o.responsesPreludeBuf.PreludeSent() {
		return
	}
	if o.Env.Stream() && o.PreludeBuf.PreludeSent() {
		sseErr := emitOpenAISSEErrorEvent(o.ContentSink, o.ProxyErr)
		if !o.SubscriptionPoolFailure {
			o.ProxyErr = sseErr
		}
		return
	}
	if o.SubscriptionPoolFailure {
		return
	}
	flushUpstreamErrorAsOpenAI(o.ContentSink, o.ProxyErr)
}

func (o *chatRescue) codexRetry(ctx context.Context) {
	s := o.svc
	r := o.HTTPRequest
	log := o.Log
	routeRes := o.RouteRes
	preludeBuf := o.PreludeBuf
	if o.codexRetryViable && !paidFallbackForbiddenForModelOnProvider(ctx, o.Decision.Provider, o.Decision.Model) && o.ProxyErr != nil && !preludeBuf.Committed() &&
		(providers.IsRetryable(o.ProxyErr) || codexOAuthCredentialRejected(o.ProxyErr) || codexSubscriptionModelRejected(o.ProxyErr)) {
		o.Credentials.RecordCodexQuotaExhaustion(ctx, r.Header, o.ProxyErr)
		subCtx := subscriptionStatePaidRescueContext(withSuppressedCodexSubscription(ctx), o.Decision.Model)
		subCtx = resolveAndInjectCredentials(subCtx, providers.ProviderOpenAI, o.Decision.Model, r.Header)
		subOpts, subEffort := o.Opts, o.EffortServed
		if subOpts.TargetModel != o.Decision.Model {
			subOpts, subEffort = switchedAttemptOptions(ctx, o.Opts, o.Decision, routeRes.EscalateEffort, s.resolveEffort)
		}
		subOpts.FastMode = fastModeForAttempt(subCtx, o.Decision.Model, providers.ProviderOpenAI)
		subAttempt, subBuildErr := o.BuildAttempt(o.Decision, subOpts, o.Marker, subCtx)
		subBindings := o.Routing.ResolveBindingsForDispatch(subCtx, o.Decision)
		switch {
		case subBuildErr != nil:
			log.Error("Codex subscription failover: preparing the Weave-key retry failed; surfacing the original error",
				"err", subBuildErr, "model", o.Decision.Model)
		case len(subBindings) == 0:
			log.Warn("Codex subscription failover: no fallback OpenAI binding available; surfacing the original error",
				"model", o.Decision.Model, "upstream_status", upstreamStatus(o.ProxyErr))
		default:
			log.Warn("Codex subscription failover: ChatGPT plan rejected the turn, retrying on Weave credits",
				"model", o.Decision.Model,
				"err", o.ProxyErr,
				"upstream_status", upstreamStatus(o.ProxyErr))
			o.codexRetryRan = true
			o.EffortServed = subEffort
			*o.RespSummary = translate.ResponseSummary{}
			subscriptionErr := o.ProxyErr
			o.WinnerIdx, o.ProxyErr = s.dispatchWithFallback(subCtx, dispatch.FailoverInputs{
				Writer:                 o.ContentSink,
				Prelude:                preludeBuf,
				InitialDecision:        o.Decision,
				Bindings:               subBindings,
				Attempt:                subAttempt,
				FlushErr:               o.flushErrAsOpenAI,
				DeferFlushOnExhaustion: o.cyberRetryViable || o.RescueArmed,
				Purpose:                routeRes.DispatchPurpose(o.Surface),
				Origin:                 routeRes.DispatchOrigin(o.Decision),
			})
			o.Bindings = subBindings
			o.codexFailoverUsed = o.ProxyErr == nil
			o.SubscriptionPoolFailure = isSubscriptionPoolError(o.ProxyErr)
			*o.cyberRefusalSeen = *o.cyberRefusalSeen || providers.IsUpstreamCyberPolicyRefusal(o.ProxyErr)
			if credentialRejectionStands(subscriptionErr, o.ProxyErr, codexOAuthCredentialRejected) {
				o.ProxyErr = subscriptionErr
				o.SubscriptionPoolFailure = false
				o.flushDeferredErr()
			}
		}
	}
}

func (o *chatRescue) claudeRetry(ctx context.Context) {
	s := o.svc
	log := o.Log
	routeRes := o.RouteRes
	preludeBuf := o.PreludeBuf
	if o.claudeRetryViable && !paidFallbackForbiddenForModelOnProvider(ctx, o.Decision.Provider, o.Decision.Model) && o.ProxyErr != nil && !preludeBuf.Committed() && (anthropicSubscriptionModelRejected(o.ProxyErr) || anthropicSubscriptionThirdPartyRefused(o.ProxyErr) || paidCapacityUnfunded(ctx) && (providers.IsRetryable(o.ProxyErr) || anthropicOAuthCredentialRejected(o.ProxyErr))) {
		subCtx := subscriptionStatePaidRescueContext(withSuppressedClaudeSubscription(ctx), o.Decision.Model)
		subCtx = resolveAndInjectCredentials(subCtx, providers.ProviderAnthropic, o.Decision.Model, o.HTTPRequest.Header)
		subOpts := o.Opts
		subOpts.FastMode = fastModeForAttempt(subCtx, o.Decision.Model, providers.ProviderAnthropic)
		subAttempt, subBuildErr := o.BuildAttempt(o.Decision, subOpts, o.Marker, subCtx)
		subBindings := o.Routing.ResolveBindingsForDispatch(subCtx, o.Decision)
		switch {
		case subBuildErr != nil:
			log.Error("Claude subscription failover: preparing the Weave-key retry failed; surfacing the original error",
				"err", subBuildErr, "model", o.Decision.Model)
		case len(subBindings) == 0:
			log.Warn("Claude subscription failover: no fallback Anthropic binding available; surfacing the original error",
				"model", o.Decision.Model, "upstream_status", upstreamStatus(o.ProxyErr))
		default:
			log.Warn("Claude subscription failover: subscription rejected the turn, retrying on Weave credits",
				"model", o.Decision.Model,
				"err", o.ProxyErr,
				"upstream_status", upstreamStatus(o.ProxyErr))
			o.claudeRetryRan = true
			*o.RespSummary = translate.ResponseSummary{}
			o.WinnerIdx, o.ProxyErr = s.dispatchWithFallback(subCtx, dispatch.FailoverInputs{
				Writer:                 o.ContentSink,
				Prelude:                preludeBuf,
				InitialDecision:        o.Decision,
				Bindings:               subBindings,
				Attempt:                subAttempt,
				FlushErr:               o.flushErrAsOpenAI,
				DeferFlushOnExhaustion: o.cyberRetryViable || o.RescueArmed,
				Purpose:                routeRes.DispatchPurpose(o.Surface),
				Origin:                 routeRes.DispatchOrigin(o.Decision),
			})
			o.Bindings = subBindings
			o.claudeFailoverUsed = o.ProxyErr == nil
			o.SubscriptionPoolFailure = isSubscriptionPoolError(o.ProxyErr)
			*o.cyberRefusalSeen = *o.cyberRefusalSeen || providers.IsUpstreamCyberPolicyRefusal(o.ProxyErr)
		}
	}

	if ((o.codexRetryViable && !o.codexRetryRan) || (o.claudeRetryViable && !o.claudeRetryRan)) &&
		!o.cyberRetryViable && !o.RescueArmed && o.ProxyErr != nil && !preludeBuf.Committed() {
		o.flushDeferredErr()
	}
}

func (o *chatRescue) cyberRetry(ctx context.Context) {
	s := o.svc
	r := o.HTTPRequest
	log := o.Log
	routeRes := o.RouteRes
	preludeBuf := o.PreludeBuf
	if o.cyberRetryViable && o.ProxyErr != nil && !preludeBuf.Committed() &&
		providers.IsUpstreamCyberPolicyRefusal(o.ProxyErr) {
		retryOpts, retryEffort := switchedAttemptOptions(ctx, o.Opts, o.cyberRetryTarget, routeRes.EscalateEffort, s.resolveEffort)
		retryCtx := resolveAndInjectCredentials(subscriptionStatePaidRescueContext(ctx, o.cyberRetryTarget.Model), o.cyberRetryTarget.Provider, o.cyberRetryTarget.Model, r.Header)
		retryOpts.FastMode = fastModeForAttempt(retryCtx, o.cyberRetryTarget.Model, o.cyberRetryTarget.Provider)
		retryBindings := o.Routing.ResolveBindingsForDispatch(retryCtx, o.cyberRetryTarget)
		retryMarker := o.Markers.WithFailureDetail(ctx, o.Markers.CyberRefusal(ctx, o.cyberRetryTarget.Model))
		retryAttempt, retryBuildErr := o.BuildAttempt(o.cyberRetryTarget, retryOpts, retryMarker, retryCtx)
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
				"failed_model", o.PrimaryModel,
				"failed_provider", o.PrimaryProvider,
				"fallback_model", o.cyberRetryTarget.Model,
				"fallback_provider", o.cyberRetryTarget.Provider)
			o.cyberRetryRan = true
			o.EffortServed = retryEffort
			*o.RespSummary = translate.ResponseSummary{}
			o.WinnerIdx, o.ProxyErr = s.dispatchWithFallback(retryCtx, dispatch.FailoverInputs{
				Writer:                 o.ContentSink,
				Prelude:                preludeBuf,
				InitialDecision:        o.cyberRetryTarget,
				Bindings:               retryBindings,
				Attempt:                retryAttempt,
				FlushErr:               o.flushErrAsOpenAI,
				DeferFlushOnExhaustion: o.RescueArmed,
				Purpose:                routeRes.DispatchPurpose(o.Surface),
				Origin:                 routeRes.RescueOrigin(),
			})
			o.SubscriptionPoolFailure = isSubscriptionPoolError(o.ProxyErr)
			o.Decision = o.cyberRetryTarget
			o.Bindings = retryBindings
			o.Marker = retryMarker
		}
	}
	if o.cyberRetryViable && !o.cyberRetryRan && !o.RescueArmed && o.ProxyErr != nil && !preludeBuf.Committed() {
		o.flushDeferredErr()
	}
}

func (o *chatRescue) finish(ctx context.Context) {
	o.ProxyErr = asCompactionUnservable(o.RouteRes.TurnType, o.Decision, o.ProxyErr)
	if (o.SiblingRescueOwed || o.OverflowRescueOnly) && o.ProxyErr != nil && !o.PreludeBuf.Committed() {
		o.flushDeferredErr()
	}

	if subscriptionCredentialFallbackUsed(ctx) {
		switch o.Decision.Provider {
		case providers.ProviderOpenAI:
			o.codexFailoverUsed = true
		case providers.ProviderAnthropic:
			o.claudeFailoverUsed = true
		}
	}
	o.FinalProvider = o.ServedProvider(o.Decision.Provider)
	o.Decision.Provider = o.FinalProvider
	o.FailoverUsed = o.FinalProvider != o.PrimaryProvider || o.codexFailoverUsed || o.claudeFailoverUsed || o.SiblingFailoverUsed
	o.FailoverAttempted = o.FailoverUsed || o.codexRetryRan || o.claudeRetryRan || o.SiblingRescueRan
}
