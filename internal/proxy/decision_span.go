package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type decisionSpanSurface int

const (
	decisionSpanWithTaskArmAndSubscriber decisionSpanSurface = iota
	decisionSpanBlindExperimentOnly
)

type decisionSpan struct {
	surface    decisionSpanSurface
	facts      requestcontext.RequestFacts
	feats      translate.RoutingFeatures
	decision   router.Decision
	routeRes   *turnLoopResult
	taskArm    taskArmTurn
	sessionKey [sessionpin.SessionKeyLen]byte
	embedInput string
	routeMs    int64
	reqPricing catalog.Pricing
	actPricing catalog.Pricing
	start      time.Time
}

func recordDecisionSpan(ctx context.Context, clock auth.Clock, in decisionSpan) {
	facts, feats, decision, routeRes := in.facts, in.feats, in.decision, in.routeRes
	pinTier := routeRes.PinTier
	reqDecisionPricing := in.reqPricing.ForInputTokens(feats.Tokens)
	actDecisionPricing := in.actPricing.ForInputTokens(feats.Tokens)
	builder := telemetry.NewAttrBuilder(46).
		String("request_id", facts.RequestID).
		String("external_id", facts.ExternalID)
	if in.surface == decisionSpanWithTaskArmAndSubscriber {
		builder.String("router_user_id", auth.UserIDFrom(ctx))
	}
	builder.
		String("client.device_id", facts.Client.DeviceID).
		String("client.account_id", facts.Client.AccountID).
		String("client.session_id", facts.Client.SessionID).
		String("client.user_agent", facts.Client.UserAgent).
		String("client.app", facts.Client.TelemetryClientApp()).
		String("rollout_id", policyRolloutIDFromContext(ctx)).
		String("requested.model", feats.Model).
		String("decision.model", decision.Model).
		String("decision.provider", decision.Provider).
		String("decision.reason", decision.Reason).
		Bool("routing.policy_fallback", routeRes.PolicyFallback).
		Bool("routing.sticky_hit", routeRes.StickyHit).
		Bool("routing.session_pin_hit", pinTier == "in_proc" || pinTier == "postgres").
		String("routing.session_pin_tier", pinTier).
		Int64("routing.session_pin_age_s", routeRes.PinAgeSec).
		String("routing.turn_type", string(routeRes.TurnType)).
		String("routing.embed_input", in.embedInput).
		Int64("routing.estimated_input_tokens", int64(feats.Tokens)).
		Float64("catalog.requested_input_per_1m", reqDecisionPricing.InputUSDPer1M).
		Float64("catalog.requested_output_per_1m", reqDecisionPricing.OutputUSDPer1M).
		Float64("catalog.actual_input_per_1m", actDecisionPricing.InputUSDPer1M).
		Float64("catalog.actual_output_per_1m", actDecisionPricing.OutputUSDPer1M).
		Int64("latency.route_ms", in.routeMs)
	if in.surface == decisionSpanWithTaskArmAndSubscriber {
		applySubscriptionDecisionSpanTelemetry(ctx, builder, feats.Model, decision.Model)
	}
	applyServingSpanAttrs(ctx, builder)
	applySidecarAttrs(builder, *routeRes)
	applyPlannerAttrs(builder, *routeRes)
	applyRoutingStateAttrs(builder, *routeRes, decision.ServedIdentity(), in.sessionKey)
	applyCallerRoutingAttrs(ctx, builder)
	if in.surface == decisionSpanWithTaskArmAndSubscriber {
		applyExperimentAttrs(ctx, builder, routeRes, in.taskArm, feats.Model, routeRes.Decision.Model)
	} else {
		applyBlindExperimentOutcomeAttrs(ctx, builder, routeRes, feats.Model, routeRes.Decision.Model)
	}
	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.decision",
		Start: in.start,
		End:   clock.Now(),
		Attrs: builder.Build(),
	})
	telemetry.Flush(ctx)
}
