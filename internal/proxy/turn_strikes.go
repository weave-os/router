package proxy

import (
	"context"
	"strings"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func (s *Service) strikeRescuedModelsForTurn(ctx context.Context, proxyErr error, served router.Decision, hardPinned bool, sessionKey [sessionpin.SessionKeyLen]byte, role, pinRole string) {
	store, ok := s.pinStore.(sessionpin.TurnStrikeStore)
	if !ok || proxyErr != nil || hardPinned || sessionKey == ([sessionpin.SessionKeyLen]byte{}) ||
		strings.HasPrefix(served.Reason, translate.ReasonUserForceModel) {
		return
	}
	models := requestcontext.TurnFailedModels(ctx)
	log := observability.FromContext(ctx)
	for _, model := range models {
		if model == served.Model {
			continue
		}
		for _, strikeRole := range demotionRoles(role, pinRole) {
			if err := store.StrikeTurnModel(context.WithoutCancel(ctx), sessionKey, strikeRole, model); err != nil {
				log.Error("agent-turn model strike failed", "err", err, "role", strikeRole, "model", model)
			}
		}
		log.Info("Rescued model kept out for the rest of the agent turn", "model", model, "served_model", served.Model)
	}
}

func (s *Service) clearTurnStrikes(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, pinRole string) {
	store, ok := s.pinStore.(sessionpin.TurnStrikeStore)
	if !ok {
		return
	}
	for _, role := range demotionRoles(pinRole, pinRole) {
		if err := store.ClearTurnStrikes(context.WithoutCancel(ctx), sessionKey, role); err != nil {
			observability.FromContext(ctx).Error("agent-turn strike clear failed", "err", err, "role", role)
		}
	}
}

func sessionDemotedModels(ctx context.Context, s *Service, env *translate.RequestEnvelope, res *routingpolicy.TurnRoute, pin, hmmHistory sessionpin.Pin) []string {
	demoted := candidates.MergeSessionStrikes(pin.DemotedModels, hmmHistory.DemotedModels)
	turnStrikes := candidates.MergeSessionStrikes(pin.TurnStrikes, hmmHistory.TurnStrikes)
	if len(turnStrikes) == 0 {
		return demoted
	}
	if routingEndsWithUserPrompt(ctx, env) {
		s.clearTurnStrikes(ctx, res.SessionKey, res.PinRole)
		return demoted
	}
	return candidates.MergeSessionStrikes(demoted, turnStrikes)
}
