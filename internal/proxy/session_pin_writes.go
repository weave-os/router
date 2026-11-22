package proxy

import (
	"context"
	"time"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/bandswap"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"

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
	switch routingpolicy.AuthoritativePinTier(res.PinTier) {
	case routingpolicy.PinTierAuthoritativeExcludedPin,
		routingpolicy.PinTierAuthoritativeUpgradeConfidenceLow,
		routingpolicy.PinTierAuthoritativeDowngradeConfidenceLow,
		routingpolicy.PinTierAuthoritativeDowngradeHysteresis:
		return true
	default:
		return false
	}
}

func pinAge(p sessionpin.Pin, now time.Time) int64 { return routingpolicy.PinAge(p, now) }
