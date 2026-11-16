package proxy

import (
	"context"
	"net/http"
	"time"
	"weave-os/router/internal/observability"
	randomrouter "weave-os/router/internal/random_router"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
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
func routeRandomTurn(s *Service, ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures, headers http.Header, req router.Request) (turnLoopResult, bool, error) {
	if randomBenchmarkFrom(ctx) == nil {
		return turnLoopResult{}, false, nil
	}
	res := turnLoopResult{Strategy: router.StrategyRandomBenchmark, TurnType: turntype.Detect(env, feats, subAgentHintForRequest(ctx, headers.Get("x-weave-subagent-type")), openCodeCaller(ClientIdentityFrom(ctx))), InstallationID: installationIDFromContext(ctx), PinTier: "random_benchmark"}
	if !randomBenchmarkHeaders(headers) {
		return res, true, randomrouter.ErrUnavailable
	}
	req = withRoutingTranslationRequirements(ctx, env, req)
	var err error
	req, err = s.applyTranslationPlan(ctx, req)
	if err != nil {
		return res, true, err
	}
	req.AutomaticExcludedModels = s.globalAutomaticExcludedModels(ctx)
	res.Decision, err = routeRandomBenchmark(s, ctx, req)
	return res, true, err
}

func (s *Service) recordTurnUsage(ctx context.Context, res turnLoopResult, servedProvider, servedModel string, in, out, cacheCreation, cacheRead int, outputLimitReached bool) {
	if randomBenchmarkFrom(ctx) != nil {
		return
	}
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
		EndedAt:             time.Now(),
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
