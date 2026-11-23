package proxy

import (
	"context"
	"time"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/capture"
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
	extendedAttrs       bool
	facts               requestcontext.RequestFacts
	feats               translate.RoutingFeatures
	decision            router.Decision
	provider            string
	routeRes            *routingpolicy.TurnRoute
	taskArm             taskArmTurn
	sessionKey          [sessionpin.SessionKeyLen]byte
	respSummary         translate.ResponseSummary
	refusal             *refusalObserver
	extractor           *telemetry.UsageExtractor
	inputTokens         int
	outputTokens        int
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
	effortServed        dispatch.EffortResolution
	captureMode         capture.Mode
	strategyPorts       continuity.StrategyPorts
	strategy            router.Strategy
	subscriptionAttrs   func(*telemetry.AttrBuilder)
}

func recordUpstreamSpan(ctx context.Context, clock auth.Clock, span upstreamSpan) (*telemetry.AttrBuilder, observationContext) {
	builder := upstreamSpanAttrs(ctx, clock, span)
	if span.captureMode == capture.Off {
		builder.Int64("request.message_count", int64(span.feats.MessageCount)).
			Bool("request.has_tools", span.feats.HasTools)
	}
	applyPlannerAttrs(builder, *span.routeRes)
	applyRoutingStateAttrs(builder, *span.routeRes, span.decision.ServedIdentity(), span.sessionKey)
	applyCallerRoutingAttrs(ctx, builder)
	if span.extendedAttrs {
		applyExperimentAttrs(ctx, builder, span.routeRes, span.taskArm, span.feats.Model, span.decision.Model)
	} else {
		applyBlindExperimentOutcomeAttrs(ctx, builder, span.routeRes, span.feats.Model, span.decision.Model)
	}
	applyEffortAttrs(builder, span.effortServed)
	span.subscriptionAttrs(builder)
	addTimingAttrs(ctx, builder)
	obs := buildObservationContext(span.strategyPorts, ctx, span.strategy, span.decision, span.routeRes.Fresh, span.captureMode)
	obs.applySpanAttrs(builder)
	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.upstream",
		Start: span.proxyStart,
		End:   clock.Now(),
		Attrs: builder.Build(),
	})
	return builder, obs
}

func upstreamSpanAttrs(ctx context.Context, clock auth.Clock, span upstreamSpan) *telemetry.AttrBuilder {
	facts, decision := span.facts, span.decision
	promptTokens := catalog.PromptTokens(span.inputTokens, span.cacheCreation, span.cacheRead, decision.Provider)
	builder := telemetry.NewAttrBuilder(41).
		String("request_id", facts.RequestID).
		String("external_id", facts.ExternalID)
	if span.extendedAttrs {
		builder.String("router_user_id", auth.UserIDFrom(ctx))
	}
	builder.
		String("client.device_id", facts.Client.DeviceID).
		String("client.account_id", facts.Client.AccountID).
		String("client.session_id", facts.Client.SessionID).
		String("client.user_agent", facts.Client.UserAgent).
		String("client.app", facts.Client.TelemetryClientApp()).
		String("rollout_id", policyRolloutIDFromContext(ctx)).
		String("requested.model", span.feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", span.provider).
		String("decision.reason", decision.Reason).
		String("routing.turn_type", string(span.routeRes.TurnType))
	if span.extendedAttrs {
		builder.
			String("upstream.finish_reason", span.respSummary.UpstreamFinishReason).
			String("upstream.stop_reason", span.respSummary.StopReason)
	}
	if span.refusal != nil {
		builder.
			Bool("upstream.refusal", span.refusal.refused).
			String("upstream.refusal_category", span.refusal.category)
	}
	builder.
		Int64("usage.input_tokens", int64(span.inputTokens)).
		Int64("usage.output_tokens", int64(span.outputTokens)).
		Int64("usage.cache_creation_input_tokens", int64(span.cacheCreation)).
		Int64("usage.cache_read_input_tokens", int64(span.cacheRead))
	if span.extendedAttrs {
		builder.
			Int64("usage.cache_creation_1h_input_tokens", int64(span.usageMods.CacheCreation1h)).
			String("usage.speed", string(span.extractor.Speed())).
			String("usage.inference_geo", string(span.extractor.InferenceGeo()))
	}
	builder.
		Float64("cost.requested_input_usd", span.requestedInputCost).
		Int64("cost.baseline_warm_prefill_tokens", int64(span.baselineWarmPrefill)).
		Float64("cost.requested_output_usd", catalog.EffectiveOutputCost(promptTokens, span.outputTokens, span.reqPricing, span.usageMods)).
		Float64("cost.actual_input_usd", catalog.EffectiveInputCost(span.inputTokens, span.cacheCreation, span.cacheRead, span.actPricing, decision.Provider, span.usageMods)).
		Float64("cost.actual_output_usd", catalog.EffectiveOutputCost(promptTokens, span.outputTokens, span.actPricing, span.usageMods)).
		Bool("cost.subscription_served", span.subscriptionServed)
	if span.extendedAttrs {
		builder.Bool("cost.fast_mode", span.fastServed)
	}
	builder.
		Int64("latency.upstream_ms", span.proxyMs).
		Int64("latency.total_ms", clock.Now().Sub(span.requestStart).Milliseconds()).
		Int64("upstream.status_code", int64(upstreamStatus(span.proxyErr))).
		Bool("routing.cross_format", span.crossFormat)
	if d := span.dispatch; d != nil {
		builder.
			String("dispatch.primary_provider", d.primaryProvider).
			String("dispatch.primary_model", d.primaryModel).
			String("dispatch.final_provider", span.provider).
			Int64("dispatch.fallback_attempts", int64(d.fallbackAttempts)).
			Bool("dispatch.failover_used", d.failoverUsed).
			Bool("dispatch.failover_attempted", d.failoverAttempted)
		d.surfaceAttrs(builder)
	}
	return builder
}
