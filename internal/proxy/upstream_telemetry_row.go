package proxy

import (
	"context"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/telemetry/turnrecord"
)

func applyServedTurnTelemetry(ctx context.Context, ports continuity.StrategyPorts, tel *turnrecord.InsertTelemetryParams, routeRes routingpolicy.TurnRoute, decision router.Decision, effortServed dispatch.EffortResolution) {
	applyServedGroupTelemetry(ports, ctx, tel, routeRes, decision)
	applyPlannerTelemetry(tel, routeRes)
	applyEffortTelemetry(tel, effortServed)
	applyAuthorityShadowTelemetry(tel, routeRes)
	applyBlindExperimentTelemetry(ctx, tel, &routeRes)
	applyPolicyPinTelemetry(ctx, tel, decision.Metadata)
	applySubscriberTelemetry(ctx, tel)
	applyAgreementShadowTelemetry(ports, ctx, tel, routeRes)
}

func applyTelemetrySettlement(tel *turnrecord.InsertTelemetryParams, billable bool, settlement subscriberSettlementState) {
	if billable {
		applySubscriberSettlementTelemetry(settlement, tel)
	} else {
		markSubscriberTelemetryUnsettled(tel)
	}
}
