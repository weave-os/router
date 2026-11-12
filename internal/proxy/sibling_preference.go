package proxy

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

func (s *Service) rememberSuccessfulSibling(ctx context.Context, turn turnLoopResult, primary, served router.Decision, proxyErr error, startedAt time.Time) {
	store, supported := s.pinStore.(sessionpin.SiblingPreferenceStore)
	failures := turnFailuresFromContext(ctx)
	if !supported || failures == nil || proxyErr != nil || turn.HardPinned || turn.CallerModelPassthrough ||
		turn.SiblingPreferenceKey == ([sessionpin.SessionKeyLen]byte{}) || turn.InstallationID == uuid.Nil || isUserForcedReason(served.Reason) {
		return
	}
	primary = rescueBasisForTurn(primary, turn)
	failures.mu.Lock()
	failedSiblings := slices.Clone(failures.failedSiblings)
	failures.mu.Unlock()
	group, displaced, ok := router.SiblingPreferenceFromSuccess(primary.Metadata, served.Model, failedSiblings)
	if !ok {
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
	if err := store.ClearSiblingWinnerExclusions(context.Background(), turn.SessionKey, strategyForTurnLoopResult(turn), demotionRoles(stickyStateRole(turn), turn.PinRole), served.Model, startedAt); err != nil {
		log.Error("Session sibling winner exclusion clear failed", "err", err, "group", group, "model", served.Model)
	}

	log.Info("Successful sibling preferred for session group", "group", group, "model", served.Model, "displaced_models", displaced)
}
