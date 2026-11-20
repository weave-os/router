package proxy

import (
	"context"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
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
func (s *Service) recordTurnUsage(ctx context.Context, res turnLoopResult, servedProvider, servedModel string, in, out, cacheCreation, cacheRead int, outputLimitReached bool) {
	if s.pinStore == nil || res.HardPinned || res.CallerModelPassthrough {
		return
	}
	if isHMMTurn(res) {
		s.recordHMMTurnHistory(ctx, res, servedProvider, servedModel, in, out, cacheCreation, cacheRead, outputLimitReached)
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
		MessageCount:        res.MessageCount,
		CacheWriteTTL:       res.CacheWriteTTL,
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

func (s *Service) recordHMMTurnHistory(ctx context.Context, res turnLoopResult, servedProvider, servedModel string, in, out, cacheCreation, cacheRead int, outputLimitReached bool) {
	sessionPins := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategyForTurnLoopResult(res))
	if servedModel == "" || res.InstallationID == uuid.Nil {
		return
	}
	var zeroKey [sessionpin.SessionKeyLen]byte
	if res.SessionKey == zeroKey {
		return
	}
	hasUsage := in != 0 || out != 0 || cacheCreation != 0 || cacheRead != 0
	strategyCtx := strategyContext(ctx, strategyForTurnLoopResult(res))
	historyProvider := servedProvider
	if !hasUsage {
		if prior := sessionPins.LoadHMMHistory(strategyCtx, res.SessionKey, res.PinRole); prior.Provider != "" {
			historyProvider = prior.Provider
		}
	}
	role := hmmHistoryRole(res.PinRole)
	sessionPins.Upsert(strategyCtx, sessionpin.Pin{
		SessionKey:     res.SessionKey,
		Role:           role,
		InstallationID: res.InstallationID,
		Provider:       historyProvider,
		Reason:         hmmHistoryStoredReason(res),
		Strategy:       strategyForTurnLoopResult(res),
		TurnCount:      1,
		PinnedUntil:    pinExpiry(hmmHistoryReason, s.clock.Now()),
	})
	if !hasUsage {
		return
	}
	now := s.clock.Now()
	if err := s.pinStore.UpdateUsage(strategyCtx, res.SessionKey, role, sessionpin.Usage{
		Strategy:            strategyForTurnLoopResult(res),
		InputTokens:         in,
		CachedReadTokens:    cacheRead,
		CachedWriteTokens:   cacheCreation,
		OutputTokens:        out,
		OutputLimitReached:  outputLimitReached,
		EndedAt:             now,
		ServedModel:         servedModel,
		ServedProvider:      servedProvider,
		PriorServedModel:    res.PriorServedModel,
		SessionEverSwitched: res.SessionEverSwitched,
		MessageCount:        res.MessageCount,
		CacheWriteTTL:       res.CacheWriteTTL,
	}); err != nil {
		observability.Get().Error("HMM switch-history writeback failed", "err", err)
	}
	s.recordTokenizerObservation(ctx, res, role, servedProvider, in, cacheCreation, cacheRead)
}
