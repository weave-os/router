package proxy

import (
	"context"
	"log/slog"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/telemetry/spans"
	"weave-os/router/internal/translate"
)

func reloadUsageBypassSession(ctx context.Context, s *Service, strategy router.Strategy, env *translate.RequestEnvelope, apiKeyID, model string, routeRes *routingpolicy.TurnRoute) {
	if s.pinStore == nil || isUnpinnedScoredTurn(routeRes.TurnType) {
		return
	}
	sessionKey := deriveSessionKeyForRequest(ctx, env, apiKeyID)
	role := roleForTier(catalog.TierFor(model))
	sessionPins := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy)
	pin, _ := sessionPins.Load(ctx, sessionKey, role)
	routeRes.SessionKey = sessionKey
	applySwitchHistory(routeRes, pin, loadSwitchHistory(ctx, s.strategyPorts, sessionPins, sessionKey, role), routingpolicy.NewForceModelPins(s.pinStore, s.clock.Now).LoadHistory(ctx, sessionKey, role))
}

func rerouteAfterUsageBypass(ctx context.Context, s *Service, log *slog.Logger, strategy router.Strategy, req router.Request, routeRes *routingpolicy.TurnRoute) error {
	rerouteCtx, rerouteSpan := spans.StartRoutingSpan(ctx, strategy, req)
	decision, err := s.routeFor(rerouteCtx, strategy, req)
	spans.FinishRoutingSpan(rerouteSpan, decision, err)
	if err != nil {
		log.Error("Reroute after usage-bypass failure failed", "err", err)
		return err
	}
	routeRes.Decision, routeRes.UserForced = decision, false
	routeRes.Fresh = decision
	return nil
}
