package proxy

import (
	"context"
	"math"
	"time"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/planner"
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
	routeRes   *routingpolicy.TurnRoute
	taskArm    taskArmTurn
	sessionKey [sessionpin.SessionKeyLen]byte
	embedInput string
	routeMs    int64
	reqPricing catalog.Pricing
	actPricing catalog.Pricing
	start      time.Time
}

func recordDecisionSpan(ctx context.Context, clock auth.Clock, span decisionSpan) {
	facts, feats, decision, routeRes := span.facts, span.feats, span.decision, span.routeRes
	pinTier := routeRes.PinTier
	reqDecisionPricing := span.reqPricing.ForInputTokens(feats.Tokens)
	actDecisionPricing := span.actPricing.ForInputTokens(feats.Tokens)
	builder := telemetry.NewAttrBuilder(46).
		String("request_id", facts.RequestID).
		String("external_id", facts.ExternalID)
	if span.surface == decisionSpanWithTaskArmAndSubscriber {
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
		String("routing.embed_input", span.embedInput).
		Int64("routing.estimated_input_tokens", int64(feats.Tokens)).
		Float64("catalog.requested_input_per_1m", reqDecisionPricing.InputUSDPer1M).
		Float64("catalog.requested_output_per_1m", reqDecisionPricing.OutputUSDPer1M).
		Float64("catalog.actual_input_per_1m", actDecisionPricing.InputUSDPer1M).
		Float64("catalog.actual_output_per_1m", actDecisionPricing.OutputUSDPer1M).
		Int64("latency.route_ms", span.routeMs)
	if span.surface == decisionSpanWithTaskArmAndSubscriber {
		applySubscriptionDecisionSpanTelemetry(ctx, builder, feats.Model, decision.Model)
	}
	applyServingSpanAttrs(ctx, builder)
	applySidecarAttrs(builder, *routeRes)
	applyPlannerAttrs(builder, *routeRes)
	applyRoutingStateAttrs(builder, *routeRes, decision.ServedIdentity(), span.sessionKey)
	applyCallerRoutingAttrs(ctx, builder)
	if span.surface == decisionSpanWithTaskArmAndSubscriber {
		applyExperimentAttrs(ctx, builder, routeRes, span.taskArm, feats.Model, routeRes.Decision.Model)
	} else {
		applyBlindExperimentOutcomeAttrs(ctx, builder, routeRes, feats.Model, routeRes.Decision.Model)
	}
	telemetry.Record(ctx, telemetry.Span{
		Name:  "router.decision",
		Start: span.start,
		End:   clock.Now(),
		Attrs: builder.Build(),
	})
	telemetry.Flush(ctx)
}

func logPlannerOutcome(ctx context.Context, ports continuity.StrategyPorts, res routingpolicy.TurnRoute) {
	if !res.PlannerDecision.Evaluated() {
		return
	}
	log := observability.FromContext(ctx)
	if res.PlannerDecision.Outcome == planner.OutcomeSwitch {
		log.Info("router switched models",
			"from", res.PinModel,
			"to", res.Decision.Model,
			"reason", res.PlannerDecision.Reason,
			"expected_savings_usd", res.PlannerDecision.ExpectedSavingsUSD,
			"eviction_cost_usd", res.PlannerDecision.EvictionCostUSD,
			"threshold_usd", res.PlannerDecision.ThresholdUSD,
			"pin_cache_warm", !res.PlannerDecision.PinCacheCold,
			"handover_invoked", res.Handover.Invoked,
			"handover_fallback_to_full_history", res.Handover.FallbackToFullHistory,
			"handover_latency_ms", res.Handover.LatencyMS,
		)
		return
	}
	message := "router stayed on pinned model"
	if isHMMDecision(ports, res.Fresh) {
		message = "router stayed on previous HMM model"
	}
	log.Info(message,
		"model", res.Decision.Model,
		"pin_model", res.PinModel,
		"reason", res.PlannerDecision.Reason,
		"expected_savings_usd", res.PlannerDecision.ExpectedSavingsUSD,
		"eviction_cost_usd", res.PlannerDecision.EvictionCostUSD,
		"threshold_usd", res.PlannerDecision.ThresholdUSD,
		"pin_cache_warm", !res.PlannerDecision.PinCacheCold,
	)
}

func plannerLogFields(res routingpolicy.TurnRoute) []any {
	var pinCacheWarm any
	if res.PlannerDecision.Evaluated() {
		pinCacheWarm = !res.PlannerDecision.PinCacheCold
	}
	return []any{
		"planner_outcome", plannerOutcomeAttr(res),
		"planner_reason", res.PlannerDecision.Reason,
		"planner_expected_savings_usd", res.PlannerDecision.ExpectedSavingsUSD,
		"planner_eviction_cost_usd", res.PlannerDecision.EvictionCostUSD,
		"planner_threshold_usd", res.PlannerDecision.ThresholdUSD,
		"planner_pin_model", res.PinModel,
		"planner_fresh_model", res.Fresh.Model,
		"planner_chosen_model", res.Decision.Model,
		"planner_pin_cache_warm", pinCacheWarm,
		"cache_pin_provider", res.PinProvider,
		"cache_prefix_stable", !res.PrefixBroken,
		"cache_prior_turn_gap_ms", res.PriorTurnGapMS,
		"planner_pin_price_fallback", res.PlannerDecision.PinPriceFallback,
		"planner_fresh_price_fallback", res.PlannerDecision.FreshPriceFallback,
	}
}

func applyPlannerAttrs(b *telemetry.AttrBuilder, res routingpolicy.TurnRoute) {
	if res.PlannerDecision.Evaluated() {
		b.String("planner.outcome", plannerOutcomeAttr(res)).
			String("planner.reason", res.PlannerDecision.Reason).
			Float64("planner.expected_savings_usd", res.PlannerDecision.ExpectedSavingsUSD).
			Float64("planner.eviction_cost_usd", res.PlannerDecision.EvictionCostUSD).
			Float64("planner.threshold_usd", res.PlannerDecision.ThresholdUSD).
			String("planner.pin_model", res.PinModel).
			String("planner.fresh_model", res.Fresh.Model).
			String("planner.chosen_model", res.Decision.Model).
			Bool("planner.pin_cache_warm", !res.PlannerDecision.PinCacheCold).
			String("cache.pin_provider", res.PinProvider).
			Bool("cache.prefix_stable", !res.PrefixBroken).
			Bool("planner.pin_price_fallback", res.PlannerDecision.PinPriceFallback).
			Bool("planner.fresh_price_fallback", res.PlannerDecision.FreshPriceFallback)
		if res.PriorTurnGapMS != nil {
			b.Int64("cache.prior_turn_gap_ms", *res.PriorTurnGapMS)
		}
		if res.PlannerDecision.ShadowComputed {
			b.String("planner.shadow_outcome", plannerOutcome(res.PlannerDecision.ShadowOutcome)).
				Float64("planner.shadow_expected_savings_usd", res.PlannerDecision.ShadowExpectedSavingsUSD).
				Float64("planner.shadow_stay_cost_usd", res.PlannerDecision.ShadowStayCostUSD).
				Float64("planner.shadow_switch_cost_usd", res.PlannerDecision.ShadowSwitchCostUSD)
		}
	}
	b.Bool("handover.invoked", res.Handover.Invoked).
		Int64("handover.latency_ms", res.Handover.LatencyMS).
		Int64("handover.summary_tokens", int64(res.Handover.SummaryTokens)).
		Bool("handover.fallback_to_full_history", res.Handover.FallbackToFullHistory)
}

func plannerOutcomeAttr(res routingpolicy.TurnRoute) string {
	if !res.PlannerDecision.Evaluated() {
		return "skipped"
	}
	switch res.PlannerDecision.Outcome {
	case planner.OutcomeStay:
		return "stay"
	case planner.OutcomeSwitch:
		return "switch"
	default:
		return "skipped"
	}
}

func applySidecarAttrs(b *telemetry.AttrBuilder, res routingpolicy.TurnRoute) {
	if res.Fresh.Metadata == nil {
		return
	}
	if st := res.Fresh.Metadata.SidecarTimings; st != nil {
		if st.EmbedMs != nil {
			b.Int64("latency.embed_ms", int64(math.Round(*st.EmbedMs)))
		}
		if st.SelectMs != nil {
			b.Int64("latency.sidecar_select_ms", int64(math.Round(*st.SelectMs)))
		}
		if st.OtherMs != nil {
			b.Int64("latency.sidecar_other_ms", int64(math.Round(*st.OtherMs)))
		}
	}
	if ss := res.Fresh.Metadata.SidecarStats; ss != nil {
		if ss.EmbedCacheHits != nil {
			b.Int64("routing.embed_cache_hits", *ss.EmbedCacheHits)
		}
		if ss.EmbedCacheMisses != nil {
			b.Int64("routing.embed_cache_misses", *ss.EmbedCacheMisses)
		}
		if ss.EmbedCacheEvictions != nil {
			b.Int64("routing.embed_cache_evictions", *ss.EmbedCacheEvictions)
		}
		if ss.RoutesInflight != nil {
			b.Int64("routing.sidecar_inflight", *ss.RoutesInflight)
		}
		if ss.OverrunsLive != nil {
			b.Int64("routing.sidecar_overruns_live", *ss.OverrunsLive)
		}
	}
}

func applyRoutingStateAttrs(
	b *telemetry.AttrBuilder,
	res routingpolicy.TurnRoute,
	servedIdentity string,
	sessionKey [sessionpin.SessionKeyLen]byte,
) {
	b.String("routing.session_key", sessionKeyHex(sessionKey)).
		String("routing.pin_role", res.PinRole).
		String("routing.prior_served_model", res.PriorServedModel).
		Bool("routing.model_changed", res.PriorServedModel != "" && res.PriorServedModel != servedIdentity).
		Bool("routing.llm_escalation_applied", res.Escalation().LLMApplied())
}
