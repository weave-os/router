package proxy

import (
	"context"
	"net/http"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

func (s *Service) reportPolicyOutcome(ctx context.Context, res routingpolicy.TurnRoute, decision router.Decision, effort dispatch.EffortResolution, finalProvider string, servedFast bool, estimatedInputTokens, inputTokens, outputTokens, cacheCreation, cacheRead int, usageMods catalog.UsageModifiers, routeMs, proxyMs int64, proxyErr error, response *policyOutcomeResponse) {
	routeDecision, routeMetadata, reporter, ok := s.policyOutcomeRoute(res, decision)
	if !ok {
		return
	}
	organizationID, _ := ctx.Value(requestcontext.ExternalIDContextKey{}).(string)
	installationID, _ := ctx.Value(requestcontext.InstallationIDContextKey{}).(string)
	classifier := s.strategyPorts.Facts.ClassifierTelemetryOf(routeMetadata)
	outcome := routingpolicy.PolicyOutcome{
		Route: routeMetadata, Selected: routeDecision, ServedModel: decision.Model, ServedProvider: finalProvider,
		OrganizationID: organizationID, InstallationID: installationID,
		ClientApp: requestcontext.ClientIdentityFrom(ctx).TelemetryClientApp(), RolloutID: policyRolloutIDFromContext(ctx),
		CaptureMode: s.effectiveCaptureMode(ctx).String(), RequestTrainingAllowed: policyTrainingAllowedForRequest(ctx),
		ClassifierArtifactID: classifier.ArtifactID, ClassifierArtifactSHA256: classifier.ArtifactSHA256,
		Effort: routingpolicy.PolicyOutcomeEffort{Arm: effort.Arm, Selected: effort.Selected, Sent: effort.Sent, Source: effort.Source, Mismatch: effort.Mismatch()},
		Status: upstreamStatus(proxyErr), Err: proxyErr,
		EstimatedInputTokens: estimatedInputTokens, InputTokens: inputTokens, OutputTokens: outputTokens,
		CacheCreationTokens: cacheCreation, CacheReadTokens: cacheRead,
		RouteLatencyMs: routeMs, UpstreamLatencyMs: proxyMs, TurnType: string(res.TurnType), StickyHit: res.StickyHit,
	}
	if outcome.AuthoritativeModelMismatch() {
		log := observability.FromContext(ctx)
		if isAuthoritativePinHoldMismatch(res, decision) {
			log.Info(
				"Authoritative policy model did not match served model",
				"route_id", routeMetadata.RouteID,
				"selected_model", routeDecision.Model,
				"served_model", decision.Model,
				"pin_tier", res.PinTier,
			)
		} else {
			log.Error(
				"Authoritative policy model did not match served model",
				"route_id", routeMetadata.RouteID,
				"selected_model", routeDecision.Model,
				"served_model", decision.Model,
			)
		}
	}
	if outcome.Effort.Mismatch {
		observability.FromContext(ctx).Warn(
			"Selected effort did not match the effort sent upstream",
			"route_id", routeMetadata.RouteID,
			"served_model", decision.Model,
			"arm_effort", effort.Arm,
			"selected_effort", effort.Selected,
			"sent_effort", effort.Sent,
			"effort_source", effort.Source,
		)
	}
	payload := outcome.Payload()
	if outcome.TrainingAllowed() && response != nil {
		payload["response_body_truncated"] = response.Truncated
		if !response.Truncated {
			payload["response_text"] = translate.AnthropicClientResponseText(response.Body)
		}
	}
	if price, ok := servedPricing(finalProvider, decision.Model, servedFast); ok {
		inputCost := catalog.EffectiveInputCost(inputTokens, cacheCreation, cacheRead, price, finalProvider, usageMods)
		outputCost := catalog.EffectiveOutputCost(catalog.PromptTokens(inputTokens, cacheCreation, cacheRead, finalProvider), outputTokens, price, usageMods)
		payload["cost_usd"] = inputCost + outputCost
	}
	log := observability.FromContext(ctx).With("route_id", routeMetadata.RouteID)
	if err := ctx.Err(); err != nil {
		log.Debug("Skipping policy outcome report for canceled request", "err", err)
		return
	}
	if identity, managed := requestcontext.ServingIdentityFromContext(ctx); managed {
		payload["serving_identity"] = identity
	}
	observability.SafeGoContext(ctx, log, policyOutcomeReportTimeout, "reportPolicyOutcome", func(reportCtx context.Context) {
		if err := reporter.ReportOutcome(reportCtx, payload); err != nil {
			log.Error("Policy outcome report failed", "strategy", routeMetadata.Strategy, "err", err)
		}
	})
}

func (s *Service) policyOutcomeRoute(res routingpolicy.TurnRoute, decision router.Decision) (router.Decision, *router.RoutingMetadata, policy.OutcomeReporter, bool) {
	for _, routeDecision := range []router.Decision{decision, res.Fresh} {
		routeMetadata := routeDecision.Metadata
		if routeMetadata == nil || routeMetadata.Strategy == "" || routeMetadata.RouteID == "" {
			continue
		}
		registered, ok := s.strategies[router.Strategy(routeMetadata.Strategy)]
		if !ok || registered.outcomes == nil {
			continue
		}
		return routeDecision, routeMetadata, registered.outcomes, true
	}
	return router.Decision{}, nil, nil, false
}

func (s *Service) capturePolicyOutcomeResponse(ctx context.Context, w http.ResponseWriter, res routingpolicy.TurnRoute, decision router.Decision) (http.ResponseWriter, *captureWriter) {
	if !policyTrainingAllowedForRequest(ctx) {
		return w, nil
	}
	if _, _, _, ok := s.policyOutcomeRoute(res, decision); !ok {
		return w, nil
	}
	capture := newCaptureWriter(w, policyOutcomeResponseMaxBytes)
	return capture, capture
}
