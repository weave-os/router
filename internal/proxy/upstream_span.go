package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type upstreamSpanDispatch struct {
	primaryProvider   string
	primaryModel      string
	fallbackAttempts  int
	failoverUsed      bool
	failoverAttempted bool
	surfaceAttrs      func(*telemetry.AttrBuilder)
}

type upstreamSpan struct {
	taskArmSurface      bool
	facts               requestcontext.RequestFacts
	feats               translate.RoutingFeatures
	decision            router.Decision
	provider            string
	routeRes            *turnLoopResult
	taskArm             taskArmTurn
	sessionKey          [sessionpin.SessionKeyLen]byte
	respSummary         translate.ResponseSummary
	refusal             *refusalObserver
	extractor           *telemetry.UsageExtractor
	in, out             int
	cacheCreation       int
	cacheRead           int
	usageMods           catalog.UsageModifiers
	requestedInputCost  float64
	baselineWarmPrefill int
	reqPricing          catalog.Pricing
	actPricing          catalog.Pricing
	subscriptionServed  bool
	fastServed          bool
	proxyMs             int64
	requestStart        time.Time
	proxyStart          time.Time
	proxyErr            error
	crossFormat         bool
	dispatch            *upstreamSpanDispatch
	effortServed        effortResolution
	captureMode         ContentCaptureMode
	strategyPorts       continuity.StrategyPorts
	strategy            router.Strategy
	subscriptionAttrs   func(*telemetry.AttrBuilder)
}

func recordUpstreamSpan(ctx context.Context, clock auth.Clock, in upstreamSpan) (*telemetry.AttrBuilder, observationContext) {
	builder := upstreamSpanAttrs(ctx, clock, in)
	if in.captureMode == CaptureOff {
		builder.Int64("request.message_count", int64(in.feats.MessageCount)).
			Bool("request.has_tools", in.feats.HasTools)
	}
	applyPlannerAttrs(builder, *in.routeRes)
	applyRoutingStateAttrs(builder, *in.routeRes, in.decision.ServedIdentity(), in.sessionKey)
	applyCallerRoutingAttrs(ctx, builder)
	if in.taskArmSurface {
		applyExperimentAttrs(ctx, builder, in.routeRes, in.taskArm, in.feats.Model, in.decision.Model)
	} else {
		applyBlindExperimentOutcomeAttrs(ctx, builder, in.routeRes, in.feats.Model, in.decision.Model)
	}
	applyEffortAttrs(builder, in.effortServed)
	in.subscriptionAttrs(builder)
	addTimingAttrs(ctx, builder)
	obs := buildObservationContext(in.strategyPorts, ctx, in.strategy, in.decision, in.routeRes.Fresh, in.captureMode)
	obs.applySpanAttrs(builder)
	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.upstream",
		Start: in.proxyStart,
		End:   clock.Now(),
		Attrs: builder.Build(),
	})
	return builder, obs
}

func upstreamSpanAttrs(ctx context.Context, clock auth.Clock, in upstreamSpan) *telemetry.AttrBuilder {
	facts, decision := in.facts, in.decision
	promptTokens := catalog.PromptTokens(in.in, in.cacheCreation, in.cacheRead, decision.Provider)
	builder := telemetry.NewAttrBuilder(41).
		String("request_id", facts.RequestID).
		String("external_id", facts.ExternalID)
	if in.taskArmSurface {
		builder.String("router_user_id", auth.UserIDFrom(ctx))
	}
	builder.
		String("client.device_id", facts.Client.DeviceID).
		String("client.account_id", facts.Client.AccountID).
		String("client.session_id", facts.Client.SessionID).
		String("client.user_agent", facts.Client.UserAgent).
		String("client.app", facts.Client.TelemetryClientApp()).
		String("rollout_id", policyRolloutIDFromContext(ctx)).
		String("requested.model", in.feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", in.provider).
		String("decision.reason", decision.Reason).
		String("routing.turn_type", string(in.routeRes.TurnType))
	if in.taskArmSurface {
		builder.
			String("upstream.finish_reason", in.respSummary.UpstreamFinishReason).
			String("upstream.stop_reason", in.respSummary.StopReason)
	}
	if in.refusal != nil {
		builder.
			Bool("upstream.refusal", in.refusal.refused).
			String("upstream.refusal_category", in.refusal.category)
	}
	builder.
		Int64("usage.input_tokens", int64(in.in)).
		Int64("usage.output_tokens", int64(in.out)).
		Int64("usage.cache_creation_input_tokens", int64(in.cacheCreation)).
		Int64("usage.cache_read_input_tokens", int64(in.cacheRead))
	if in.taskArmSurface {
		builder.
			Int64("usage.cache_creation_1h_input_tokens", int64(in.usageMods.CacheCreation1h)).
			String("usage.speed", string(in.extractor.Speed())).
			String("usage.inference_geo", string(in.extractor.InferenceGeo()))
	}
	builder.
		Float64("cost.requested_input_usd", in.requestedInputCost).
		Int64("cost.baseline_warm_prefill_tokens", int64(in.baselineWarmPrefill)).
		Float64("cost.requested_output_usd", catalog.EffectiveOutputCost(promptTokens, in.out, in.reqPricing, in.usageMods)).
		Float64("cost.actual_input_usd", catalog.EffectiveInputCost(in.in, in.cacheCreation, in.cacheRead, in.actPricing, decision.Provider, in.usageMods)).
		Float64("cost.actual_output_usd", catalog.EffectiveOutputCost(promptTokens, in.out, in.actPricing, in.usageMods)).
		Bool("cost.subscription_served", in.subscriptionServed)
	if in.taskArmSurface {
		builder.Bool("cost.fast_mode", in.fastServed)
	}
	builder.
		Int64("latency.upstream_ms", in.proxyMs).
		Int64("latency.total_ms", clock.Now().Sub(in.requestStart).Milliseconds()).
		Int64("upstream.status_code", int64(upstreamStatus(in.proxyErr))).
		Bool("routing.cross_format", in.crossFormat)
	if d := in.dispatch; d != nil {
		builder.
			String("dispatch.primary_provider", d.primaryProvider).
			String("dispatch.primary_model", d.primaryModel).
			String("dispatch.final_provider", in.provider).
			Int64("dispatch.fallback_attempts", int64(d.fallbackAttempts)).
			Bool("dispatch.failover_used", d.failoverUsed).
			Bool("dispatch.failover_attempted", d.failoverAttempted)
		d.surfaceAttrs(builder)
	}
	return builder
}
