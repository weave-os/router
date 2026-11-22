package proxy

import (
	"context"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func withRoutingTranslationRequirements(ctx context.Context, env *translate.RequestEnvelope, req router.Request) router.Request {
	if requirements, ok := translationRequirementsFromContext(ctx); ok {
		req.TranslationRequirements = requirements
	} else if req.TranslationRequirements.IsZero() {
		req.TranslationRequirements = env.TranslationRequirements(translationEndpointFor(env))
	}
	return req
}
func (s *Service) recordTurnUsage(ctx context.Context, res routingpolicy.TurnRoute, servedProvider, servedModel string, in, out, cacheCreation, cacheRead int, outputLimitReached bool) {
	if s.pinStore == nil || res.HardPinned || res.CallerModelPassthrough {
		return
	}
	if isHMMTurn(s.strategyPorts, res) {
		s.recordSwitchHistory(ctx, res, servedProvider, servedModel, in, out, cacheCreation, cacheRead, outputLimitReached)
		return
	}
	var zeroKey [sessionpin.SessionKeyLen]byte
	if res.SessionKey == zeroKey {
		return
	}
	if in == 0 && out == 0 && cacheCreation == 0 && cacheRead == 0 {
		return
	}
	usage := sessionpin.Usage{
		Strategy:            strategyForTurnLoopResult(res),
		InputTokens:         in,
		CachedReadTokens:    cacheRead,
		CachedWriteTokens:   cacheCreation,
		OutputTokens:        out,
		OutputLimitReached:  outputLimitReached,
		EndedAt:             s.clock.Now(),
		ServedModel:         servedModel,
		ServedProvider:      servedProvider,
		PriorServedModel:    res.PriorServedModel,
		SessionEverSwitched: res.SessionEverSwitched,
	}
	role := res.PinRole
	if isUserForcedReason(res.Decision.Reason) {
		role = forceModelHistoryRole(role)
	}
	if role == "" {
		role = sessionpin.DefaultRole
	}
	if err := s.pinStore.UpdateUsage(context.WithoutCancel(ctx), res.SessionKey, role, usage); err != nil {
		observability.Get().Error("session pin usage writeback failed", "err", err)
	}
	s.recordTokenizerObservation(ctx, res, role, servedProvider, in, cacheCreation, cacheRead)
}
