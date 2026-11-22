package proxy

import (
	"context"
	"strings"
	"time"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/bandswap"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

func (s *Service) recordHMMTurnHistory(ctx context.Context, res routingpolicy.TurnRoute, servedProvider, servedModel string, in, out, cacheCreation, cacheRead int, outputLimitReached bool) {
	if servedModel == "" || res.InstallationID == uuid.Nil || res.SessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return
	}
	strategy := strategyForTurnLoopResult(res)
	role, usageRecorded := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy).RecordHMMTurn(strategyContext(ctx, strategy), routingpolicy.HMMTurnRecord{
		SessionKey:     res.SessionKey,
		InstallationID: res.InstallationID,
		PinRole:        res.PinRole,
		Reason:         hmmHistoryStoredReason(s.strategyPorts, res),
		HistoryReason:  s.strategyPorts.CostGate.HistoryReason(),
		Usage: sessionpin.Usage{
			InputTokens:         in,
			CachedReadTokens:    cacheRead,
			CachedWriteTokens:   cacheCreation,
			OutputTokens:        out,
			OutputLimitReached:  outputLimitReached,
			ServedModel:         servedModel,
			ServedProvider:      servedProvider,
			PriorServedModel:    res.PriorServedModel,
			SessionEverSwitched: res.SessionEverSwitched,
		},
	})
	if usageRecorded {
		s.recordTokenizerObservation(ctx, res, role, servedProvider, in, cacheCreation, cacheRead)
	}
}

func (s *Service) repinOffRefusingModel(ctx context.Context, strategy router.Strategy, sessionKey [sessionpin.SessionKeyLen]byte, role string, served router.Decision, category, avoidProvider string) {
	if s.pinStore == nil {
		return
	}
	if !s.ResolveCyberRefusalRepin(ctx) {
		return
	}
	installationID := installationIDFromContext(ctx)
	if installationID == uuid.Nil {
		return
	}
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return
	}
	if strings.HasPrefix(served.Reason, translate.ReasonUserForceModel) {
		return
	}
	log := observability.FromContext(ctx)
	fbModel, fbProvider, ok := s.cyberRefusalFallback(ctx, strategy, sessionKey, role, served, avoidProvider)
	if !ok {
		log.Warn("safety refusal observed but no distinct fallback model available; not re-pinning",
			"from_model", served.Model, "fallback_model", fbModel, "refusal_category", category)
		return
	}
	if err := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy).WriteRepin(ctx, installationID, sessionKey, role, fbProvider, fbModel, reasonCyberRefusalRepin); err != nil {
		log.Error("cyber-refusal re-pin: pin upsert failed", "err", err, "from_model", served.Model, "to_model", fbModel)
		return
	}
	log.Info("safety refusal — re-pinned session off refusing model",
		"session_key", conversation.ShortSessionKey(sessionKey),
		"refusal_category", category,
		"from_model", served.Model,
		"to_model", fbModel,
		"to_provider", fbProvider)
}

func (s *Service) bandSwapServed(ctx context.Context, turnType turntype.TurnType, pin sessionpin.Pin, fresh router.Decision, hasImages bool, imageCount int, enabledProviders, excludedModels map[string]struct{}) router.Decision {
	anchor := pin.Decision()
	if s.bandSwap == nil || pin.PairedModel == "" || turnType != turntype.MainLoop {
		return anchor
	}
	if fresh.Metadata == nil || len(fresh.Metadata.Embedding) != bandswap.EmbedDim {
		return anchor
	}
	action, band, ok := s.bandSwap.PredictBand(fresh.Metadata.Embedding)
	if !ok {
		return anchor
	}
	served := candidates.BandSwapTarget(pin, band)
	if served.Model != pin.Model {
		eligibility := candidates.BandSwapEligibility{Available: s.routing.AvailableModels(), Excluded: excludedModels, EnabledProviders: enabledProviders, HasImages: hasImages, ImageCount: imageCount}
		if !eligibility.Admits(served) {
			return anchor
		}
		if _, disabled := s.globalAutomaticExcludedModels(ctx)[served.Model]; disabled {
			return anchor
		}
		if !s.engine.Clients().Has(served.Provider) {
			return anchor
		}
	}
	observability.FromContext(ctx).Info("band swap served",
		"predicted_action", action,
		"band", band,
		"served_model", served.Model,
		"served_provider", served.Provider,
		"anchor_model", pin.Model,
		"paired_model", pin.PairedModel,
	)
	return served
}

func isAuthoritativePinHoldMismatch(res routingpolicy.TurnRoute, served router.Decision) bool {
	if served.Metadata != nil {
		return false
	}
	switch pinTier(res.PinTier) {
	case pinTierAuthoritativeExcludedPin,
		pinTierAuthoritativeUpgradeConfidenceLow,
		pinTierAuthoritativeDowngradeConfidenceLow,
		pinTierAuthoritativeDowngradeHysteresis:
		return true
	default:
		return false
	}
}

func pinAge(p sessionpin.Pin, now time.Time) int64 {
	if p.FirstPinnedAt.IsZero() {
		return 0
	}
	d := now.Sub(p.FirstPinnedAt)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}
