package proxy

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
)

func supersededSiblingPin(pin sessionpin.Pin, req router.Request) bool {
	preference, remembered := req.SiblingPreferences[escalation.Group(pin.PolicyGroup)]
	return remembered && preference.Model != "" && pin.Model != preference.Model
}

func (s *Service) rememberSuccessfulSibling(ctx context.Context, turn turnLoopResult, primary, served router.Decision, proxyErr error, startedAt time.Time) {
	store, supported := s.pinStore.(sessionpin.SiblingPreferenceStore)
	failures := turnFailuresFromContext(ctx)
	if !supported || failures == nil || proxyErr != nil || turn.HardPinned || turn.CallerModelPassthrough ||
		turn.SiblingPreferenceKey == ([sessionpin.SessionKeyLen]byte{}) || turn.InstallationID == uuid.Nil || isUserForcedReason(served.Reason) {
		return
	}
	primary = rescueBasisForTurn(primary, turn)
	metadata := primary.Metadata
	if metadata == nil || !slices.Contains(metadata.SiblingModels, served.Model) {
		return
	}
	group := escalation.Group(metadata.PolicyGroup)
	if escalation.Rank(group) < 0 {
		return
	}
	failures.mu.Lock()
	var displaced []string
	for _, model := range failures.siblings {
		if model != served.Model && slices.Contains(metadata.SiblingModels, model) {
			displaced = append(displaced, model)
		}
	}
	failures.mu.Unlock()
	if len(displaced) == 0 {
		return
	}
	log := observability.FromContext(ctx)
	written, err := store.RecordSiblingPreference(context.Background(), turn.InstallationID, turn.SiblingPreferenceKey, strategyForTurnLoopResult(turn), group, served.Model, displaced, startedAt)
	if err != nil {
		log.Error("Session sibling preference write failed", "err", err, "group", group, "model", served.Model)
		return
	}
	if !written {
		return
	}
	if err := store.ClearSiblingWinnerExclusions(context.Background(), turn.SessionKey, strategyForTurnLoopResult(turn), demotionRoles(stickyStateRole(turn), turn.PinRole), served.Model); err != nil {
		log.Error("Session sibling winner exclusion clear failed", "err", err, "group", group, "model", served.Model)
	}

	log.Info("Successful sibling preferred for session group", "group", group, "model", served.Model, "displaced_models", displaced)
}
