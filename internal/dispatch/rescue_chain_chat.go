package dispatch

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

type ChatRescue struct {
	*RescueChain
	IsResponses          bool
	ResponsesPreludeBuf  *PreludeBuffer
	CyberRefusalSeen     *bool
	CyberRetryArmed      *bool
	PreOutputRescueArmed *bool

	CyberRetryTarget  router.Decision
	CyberRetryViable  bool
	CodexRetryViable  bool
	ClaudeRetryViable bool

	CodexFailoverUsed  bool
	CodexRetryRan      bool
	ClaudeFailoverUsed bool
	ClaudeRetryRan     bool
	CyberRetryRan      bool
}

func (o *ChatRescue) ArmRescues(ctx context.Context) context.Context {
	ctx = o.ArmSiblings(ctx, o.RouteRes.CallerModelPassthrough || strings.HasPrefix(o.Decision.Reason, translate.ReasonUserForceModel))
	*o.PreOutputRescueArmed = o.CodexRetryViable || o.RescueArmed
	return ctx
}

func (o *ChatRescue) DispatchPrimary(ctx context.Context) {
	o.DispatchPrimaryAttempt(ctx, o.CyberRetryViable || o.CodexRetryViable || o.ClaudeRetryViable || o.RescueArmed, nil)
	if o.SubscriptionStatePaidTargetAttempted {
		o.CodexRetryViable = false
		o.ClaudeRetryViable = false
	}
	*o.CyberRefusalSeen = *o.CyberRefusalSeen || providers.IsUpstreamCyberPolicyRefusal(o.ProxyErr)
}

func (o *ChatRescue) ArmCyberRetry(ctx context.Context, strategy router.Strategy) {
	routeRes := o.RouteRes
	_, o.ExplicitRequestedModel = catalog.ByID(o.Request.RequestedModel)
	cyberRetryEligible := o.Routing.ResolveCyberRefusalRetry(ctx) &&
		!routeRes.CallerModelPassthrough &&
		o.Decision.Provider == providers.ProviderOpenAI &&
		!strings.HasPrefix(o.Decision.Reason, translate.ReasonUserForceModel) &&
		!o.Routing.IsHardPinnedTurn(ctx, strategy, routeRes.TurnType) &&
		!billing.SubscriptionOnlyFromContext(ctx)
	if cyberRetryEligible {
		target, found := o.Routing.CyberRefusalRetryTarget(ctx, o.Decision, RescueSessionTarget{Strategy: strategy, InstallationID: routeRes.InstallationID, SessionKey: routeRes.SessionKey}, routeRes.StickyRole,
			o.OverflowEstimate, o.Env.SignatureTokenSavings(), o.OutputReserve)
		o.CyberRetryViable = found && (o.Routing.ShouldFailover(ctx) || o.Routing.GatewaySiblingAllowed(ctx, target)) &&
			(!o.Credentials.SubscriptionStateModelsEnabled(ctx) || !o.Funding.PaidFallbackForbidden(ctx, target.Model))
		o.CyberRetryTarget = target
	}
	*o.CyberRetryArmed = o.CyberRetryViable
}

func (o *ChatRescue) ArmSubscriptionRetries(ctx context.Context) {
	o.CodexRetryViable = o.Decision.Provider == providers.ProviderOpenAI &&
		o.Credentials.ServedOnCodexSubscription(ctx) &&
		!o.Funding.PaidFallbackForbiddenOnProvider(ctx, o.Decision.Provider, o.Decision.Model) &&
		o.Credentials.OpenAIFallbackKeyAvailable(ctx)
	o.ClaudeRetryViable = o.Decision.Provider == providers.ProviderAnthropic &&
		o.Credentials.ServedOnSubscription(ctx) &&
		!o.Funding.PaidFallbackForbiddenOnProvider(ctx, o.Decision.Provider, o.Decision.Model) &&
		o.Credentials.AnthropicFallbackKeyAvailable(ctx)
}

func (o *ChatRescue) FlushErrAsOpenAI(w http.ResponseWriter, err error) {
	if o.IsResponses && o.ResponsesPreludeBuf != nil && o.ResponsesPreludeBuf.PreludeSent() {
		return
	}
	if o.Env.Stream() && o.PreludeBuf.PreludeSent() {
		_ = o.Render.EmitOpenAISSEError(w, err)
		return
	}
	if o.Credentials.IsSubscriptionPoolError(err) {
		return
	}
	o.Render.FlushOpenAIError(w, err)
}

func (o *ChatRescue) FlushDeferredErr() {
	if o.DeferredErrFlushed {
		return
	}
	o.DeferredErrFlushed = true
	if o.IsResponses && o.ResponsesPreludeBuf != nil && o.ResponsesPreludeBuf.PreludeSent() {
		return
	}
	if o.Env.Stream() && o.PreludeBuf.PreludeSent() {
		sseErr := o.Render.EmitOpenAISSEError(o.ContentSink, o.ProxyErr)
		if !o.SubscriptionPoolFailure {
			o.ProxyErr = sseErr
		}
		return
	}
	if o.SubscriptionPoolFailure {
		return
	}
	o.Render.FlushOpenAIError(o.ContentSink, o.ProxyErr)
}

func (o *ChatRescue) CodexRetry(ctx context.Context) {
	r := o.HTTPRequest
	log := o.Log
	routeRes := o.RouteRes
	preludeBuf := o.PreludeBuf
	if o.CodexRetryViable && !o.Funding.PaidFallbackForbiddenOnProvider(ctx, o.Decision.Provider, o.Decision.Model) && o.ProxyErr != nil && !preludeBuf.Committed() &&
		(providers.IsRetryable(o.ProxyErr) || o.Credentials.CodexOAuthRejected(o.ProxyErr) || o.Credentials.CodexSubscriptionModelRejected(o.ProxyErr)) {
		o.Credentials.RecordCodexQuotaExhaustion(ctx, r.Header, o.ProxyErr)
		subCtx := o.Funding.PaidRescueContext(o.Credentials.SuppressCodexSubscription(ctx), o.Decision.Model)
		subCtx = o.Credentials.ResolveAndInjectCredentials(subCtx, providers.ProviderOpenAI, o.Decision.Model, r.Header)
		subOpts, subEffort := o.Opts, o.EffortServed
		if subOpts.TargetModel != o.Decision.Model {
			subOpts, subEffort = o.Attempts.SwitchedOptions(ctx, o.Opts, o.Decision, routeRes.EscalateEffort)
		}
		subOpts.FastMode = o.Attempts.FastMode(subCtx, o.Decision.Model, providers.ProviderOpenAI)
		subAttempt, subBuildErr := o.BuildAttempt(o.Decision, subOpts, o.Marker, subCtx)
		subBindings := o.Routing.ResolveBindingsForDispatch(subCtx, o.Decision)
		switch {
		case subBuildErr != nil:
			log.Error("Codex subscription failover: preparing the Weave-key retry failed; surfacing the original error",
				"err", subBuildErr, "model", o.Decision.Model)
		case len(subBindings) == 0:
			log.Warn("Codex subscription failover: no fallback OpenAI binding available; surfacing the original error",
				"model", o.Decision.Model, "upstream_status", UpstreamStatus(o.ProxyErr))
		default:
			log.Warn("Codex subscription failover: ChatGPT plan rejected the turn, retrying on Weave credits",
				"model", o.Decision.Model,
				"err", o.ProxyErr,
				"upstream_status", UpstreamStatus(o.ProxyErr))
			o.CodexRetryRan = true
			o.EffortServed = subEffort
			*o.RespSummary = translate.ResponseSummary{}
			subscriptionErr := o.ProxyErr
			o.WinnerIdx, o.ProxyErr = o.Dispatch(subCtx, FailoverInputs{
				Writer:                 o.ContentSink,
				Prelude:                preludeBuf,
				InitialDecision:        o.Decision,
				Bindings:               subBindings,
				Attempt:                subAttempt,
				FlushErr:               o.FlushErrAsOpenAI,
				DeferFlushOnExhaustion: o.CyberRetryViable || o.RescueArmed,
				Purpose:                routeRes.DispatchPurpose(o.Surface),
				Origin:                 routeRes.DispatchOrigin(o.Decision),
			})
			o.Bindings = subBindings
			o.CodexFailoverUsed = o.ProxyErr == nil
			o.SubscriptionPoolFailure = o.Credentials.IsSubscriptionPoolError(o.ProxyErr)
			*o.CyberRefusalSeen = *o.CyberRefusalSeen || providers.IsUpstreamCyberPolicyRefusal(o.ProxyErr)
			if o.Credentials.CredentialRejectionStands(subscriptionErr, o.ProxyErr, o.Credentials.CodexOAuthRejected) {
				o.ProxyErr = subscriptionErr
				o.SubscriptionPoolFailure = false
				o.FlushDeferredErr()
			}
		}
	}
}

func (o *ChatRescue) ClaudeRetry(ctx context.Context) {
	log := o.Log
	routeRes := o.RouteRes
	preludeBuf := o.PreludeBuf
	if o.ClaudeRetryViable && !o.Funding.PaidFallbackForbiddenOnProvider(ctx, o.Decision.Provider, o.Decision.Model) && o.ProxyErr != nil && !preludeBuf.Committed() && (o.Credentials.AnthropicSubscriptionModelRejected(o.ProxyErr) || o.Credentials.AnthropicThirdPartyRefused(o.ProxyErr) || o.Funding.PaidCapacityUnfunded(ctx) && (providers.IsRetryable(o.ProxyErr) || o.Credentials.AnthropicOAuthRejected(o.ProxyErr))) {
		subCtx := o.Funding.PaidRescueContext(credentials.WithSuppressedClaudeSubscription(ctx), o.Decision.Model)
		subCtx = o.Credentials.ResolveAndInjectCredentials(subCtx, providers.ProviderAnthropic, o.Decision.Model, o.HTTPRequest.Header)
		subOpts := o.Opts
		subOpts.FastMode = o.Attempts.FastMode(subCtx, o.Decision.Model, providers.ProviderAnthropic)
		subAttempt, subBuildErr := o.BuildAttempt(o.Decision, subOpts, o.Marker, subCtx)
		subBindings := o.Routing.ResolveBindingsForDispatch(subCtx, o.Decision)
		switch {
		case subBuildErr != nil:
			log.Error("Claude subscription failover: preparing the Weave-key retry failed; surfacing the original error",
				"err", subBuildErr, "model", o.Decision.Model)
		case len(subBindings) == 0:
			log.Warn("Claude subscription failover: no fallback Anthropic binding available; surfacing the original error",
				"model", o.Decision.Model, "upstream_status", UpstreamStatus(o.ProxyErr))
		default:
			log.Warn("Claude subscription failover: subscription rejected the turn, retrying on Weave credits",
				"model", o.Decision.Model,
				"err", o.ProxyErr,
				"upstream_status", UpstreamStatus(o.ProxyErr))
			o.ClaudeRetryRan = true
			*o.RespSummary = translate.ResponseSummary{}
			o.WinnerIdx, o.ProxyErr = o.Dispatch(subCtx, FailoverInputs{
				Writer:                 o.ContentSink,
				Prelude:                preludeBuf,
				InitialDecision:        o.Decision,
				Bindings:               subBindings,
				Attempt:                subAttempt,
				FlushErr:               o.FlushErrAsOpenAI,
				DeferFlushOnExhaustion: o.CyberRetryViable || o.RescueArmed,
				Purpose:                routeRes.DispatchPurpose(o.Surface),
				Origin:                 routeRes.DispatchOrigin(o.Decision),
			})
			o.Bindings = subBindings
			o.ClaudeFailoverUsed = o.ProxyErr == nil
			o.SubscriptionPoolFailure = o.Credentials.IsSubscriptionPoolError(o.ProxyErr)
			*o.CyberRefusalSeen = *o.CyberRefusalSeen || providers.IsUpstreamCyberPolicyRefusal(o.ProxyErr)
		}
	}

	if ((o.CodexRetryViable && !o.CodexRetryRan) || (o.ClaudeRetryViable && !o.ClaudeRetryRan)) &&
		!o.CyberRetryViable && !o.RescueArmed && o.ProxyErr != nil && !preludeBuf.Committed() {
		o.FlushDeferredErr()
	}
}

func (o *ChatRescue) CyberRetry(ctx context.Context) {
	r := o.HTTPRequest
	log := o.Log
	routeRes := o.RouteRes
	preludeBuf := o.PreludeBuf
	if o.CyberRetryViable && o.ProxyErr != nil && !preludeBuf.Committed() &&
		providers.IsUpstreamCyberPolicyRefusal(o.ProxyErr) {
		retryOpts, retryEffort := o.Attempts.SwitchedOptions(ctx, o.Opts, o.CyberRetryTarget, routeRes.EscalateEffort)
		retryCtx := o.Credentials.ResolveAndInjectCredentials(o.Funding.PaidRescueContext(ctx, o.CyberRetryTarget.Model), o.CyberRetryTarget.Provider, o.CyberRetryTarget.Model, r.Header)
		retryOpts.FastMode = o.Attempts.FastMode(retryCtx, o.CyberRetryTarget.Model, o.CyberRetryTarget.Provider)
		retryBindings := o.Routing.ResolveBindingsForDispatch(retryCtx, o.CyberRetryTarget)
		retryMarker := o.Markers.WithFailureDetail(ctx, o.Markers.CyberRefusal(ctx, o.CyberRetryTarget.Model))
		retryAttempt, retryBuildErr := o.BuildAttempt(o.CyberRetryTarget, retryOpts, retryMarker, retryCtx)
		switch {
		case retryBuildErr != nil:
			log.Error("Cyber-refusal retry: preparing the fallback request failed; surfacing the refusal",
				"err", retryBuildErr,
				"fallback_model", o.CyberRetryTarget.Model)
		case len(retryBindings) == 0:
			log.Warn("Cyber-refusal retry: fallback has no usable binding; surfacing the refusal",
				"fallback_model", o.CyberRetryTarget.Model,
				"fallback_provider", o.CyberRetryTarget.Provider)
		default:
			log.Warn("Cyber-refusal retry: OpenAI declined the turn on cyber policy, retrying off-vendor",
				"failed_model", o.PrimaryModel,
				"failed_provider", o.PrimaryProvider,
				"fallback_model", o.CyberRetryTarget.Model,
				"fallback_provider", o.CyberRetryTarget.Provider)
			o.CyberRetryRan = true
			o.EffortServed = retryEffort
			*o.RespSummary = translate.ResponseSummary{}
			o.WinnerIdx, o.ProxyErr = o.Dispatch(retryCtx, FailoverInputs{
				Writer:                 o.ContentSink,
				Prelude:                preludeBuf,
				InitialDecision:        o.CyberRetryTarget,
				Bindings:               retryBindings,
				Attempt:                retryAttempt,
				FlushErr:               o.FlushErrAsOpenAI,
				DeferFlushOnExhaustion: o.RescueArmed,
				Purpose:                routeRes.DispatchPurpose(o.Surface),
				Origin:                 routeRes.RescueOrigin(),
			})
			o.SubscriptionPoolFailure = o.Credentials.IsSubscriptionPoolError(o.ProxyErr)
			o.Decision = o.CyberRetryTarget
			o.Bindings = retryBindings
			o.Marker = retryMarker
		}
	}
	if o.CyberRetryViable && !o.CyberRetryRan && !o.RescueArmed && o.ProxyErr != nil && !preludeBuf.Committed() {
		o.FlushDeferredErr()
	}
}

func (o *ChatRescue) Finish(ctx context.Context) {
	o.ProxyErr = o.Overflow.AsCompactionUnservable(o.RouteRes.TurnType, o.Decision, o.ProxyErr)
	if (o.SiblingRescueOwed || o.OverflowRescueOnly) && o.ProxyErr != nil && !o.PreludeBuf.Committed() {
		o.FlushDeferredErr()
	}

	if o.Credentials.SubscriptionCredentialFallbackUsed(ctx) {
		switch o.Decision.Provider {
		case providers.ProviderOpenAI:
			o.CodexFailoverUsed = true
		case providers.ProviderAnthropic:
			o.ClaudeFailoverUsed = true
		}
	}
	o.FinalProvider = o.ServedProvider(o.Decision.Provider)
	o.Decision.Provider = o.FinalProvider
	o.FailoverUsed = o.FinalProvider != o.PrimaryProvider || o.CodexFailoverUsed || o.ClaudeFailoverUsed || o.SiblingFailoverUsed
	o.FailoverAttempted = o.FailoverUsed || o.CodexRetryRan || o.ClaudeRetryRan || o.SiblingRescueRan
}
