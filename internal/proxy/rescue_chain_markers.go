package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

type rescueMarkers struct {
	strategyPorts continuity.StrategyPorts
	header        http.Header
	routeRes      turnLoopResult
	usageObserver *usage.Observer
}

var _ dispatch.RescueMarkers = rescueMarkers{}

func (m rescueMarkers) SubscriptionTarget(ctx context.Context, picked, target router.Decision, pickedMarker string) string {
	return subscriptionTargetMarker(m.strategyPorts, ctx, m.header, m.routeRes, picked, target, pickedMarker)
}

func (m rescueMarkers) Sibling(ctx context.Context, failedModel, siblingModel string) string {
	return siblingMarkerFor(m.strategyPorts, ctx, m.header, m.routeRes, failedModel, siblingModel)
}

func (m rescueMarkers) CyberRefusal(ctx context.Context, targetModel string) string {
	return suppressMarkerIfRequested(ctx, m.header, modelSelectionMarkerForRequest(m.strategyPorts, ctx, m.routeRes, cyberRefusalRoutingMarkerFor(m.routeRes, targetModel), targetModel, markerReasonCyberRefusal))
}

func (m rescueMarkers) EarlyStop(ctx context.Context, targetModel string) string {
	return suppressMarkerIfRequested(ctx, m.header, modelSelectionMarkerForRequest(m.strategyPorts, ctx, m.routeRes, siblingRoutingMarkerFor(m.routeRes, markerReasonEarlyStop, targetModel), targetModel, markerReasonEarlyStop))
}

func (m rescueMarkers) Baseline(ctx context.Context, baselineModel string) string {
	return suppressMarkerIfRequested(ctx, m.header, modelSelectionMarkerForRequest(m.strategyPorts, ctx, m.routeRes, baselineRoutingMarkerFor(m.routeRes, baselineModel), baselineModel, markerReasonBaseline))
}

func (m rescueMarkers) WithFailureDetail(ctx context.Context, marker string) string {
	return withRescueDetail(marker, lastFailureDetail(attemptTraceFromContext(ctx)))
}

func (m rescueMarkers) ClaudeNotice(ctx context.Context, model, marker string) translate.RouterNotice {
	return routerNoticeForRequest(ctx, m.header, subscriptions.ProviderClaude, model, marker)
}

func (m rescueMarkers) WithOwnClaudeRejectionNudge(ctx context.Context) context.Context {
	return withOwnClaudeRejectionNudge(ctx, m.header, m.usageObserver)
}
