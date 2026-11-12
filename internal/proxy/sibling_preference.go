package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

// recordRescuedTurnOutcome keeps the pin-side bookkeeping a rescued turn
// leaves behind in one call: the strike on the failed primary, then the
// remembered winner for the conversation's group.
func recordRescuedTurnOutcome(ctx context.Context, s *Service, proxyErr error, decision router.Decision, turn turnLoopResult, primary router.Decision, startedAt time.Time) {
	s.strikeRescuedModelsForTurn(ctx, proxyErr, decision, turn.HardPinned, turn.SessionKey, stickyStateRole(turn), turn.PinRole)
	rememberSuccessfulSibling(ctx, s.pinStore, turn, primary, decision, proxyErr, startedAt)
}

// loadSiblingPreferences reads the conversation's remembered group winners for
// turns that still route automatically. It writes res.SiblingPreferenceKey so
// the write path uses the same digest. Pinned and utility paths never consult
// the result, so a store outage must not reject them: the caller defers the
// error to the routed path.
func loadSiblingPreferences(ctx context.Context, s *Service, env *translate.RequestEnvelope, apiKeyID string, installationID uuid.UUID, threadSessionKey [sessionpin.SessionKeyLen]byte, res *turnLoopResult) (map[escalation.Group]router.SiblingPreference, error) {
	store, supported := s.pinStore.(sessionpin.SiblingPreferenceStore)
	if !supported || installationID == uuid.Nil || threadSessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return nil, nil
	}
	res.SiblingPreferenceKey = deriveConversationSessionKeyForRequest(ctx, env, apiKeyID, threadSessionKey, requestcontext.SiblingPreferenceConversationKey)
	preferences, err := store.GetSiblingPreferences(ctx, installationID, res.SiblingPreferenceKey, res.Strategy)
	if err != nil {
		observability.FromContext(ctx).Error("Session sibling preferences unavailable", "err", err)
		return nil, err
	}
	return preferences, nil
}

func clearedSessionPinReason(pin sessionpin.Pin, pinFound bool) string {
	if !pinFound && pin.Model == "" && pin.Provider == "" && pin.Reason != "" {
		return pin.Reason
	}
	return ""
}

func dropForceModelClearedPin(pin sessionpin.Pin, pinFound, forceModelCleared bool) (sessionpin.Pin, bool) {
	if forceModelCleared && pinFound && isUserForcedReason(pin.Reason) {
		return sessionpin.Pin{}, false
	}
	return pin, pinFound
}

func dropSupersededSiblingPin(pin sessionpin.Pin, found bool, req router.Request, log *slog.Logger) (sessionpin.Pin, bool) {
	if found && !isUserForcedReason(pin.Reason) && sessionpin.SiblingPinSuperseded(pin, req) {
		log.Info("Session sibling preference supersedes automatic pin", "pin_model", pin.Model, "group", pin.PolicyGroup)
		return sessionpin.Pin{}, false
	}
	return pin, found
}

func siblingPreferenceReadError(err error) error {
	return fmt.Errorf("load session sibling preferences: %w", err)
}

func rememberSuccessfulSibling(ctx context.Context, pinStore sessionpin.Store, turn turnLoopResult, primary, served router.Decision, proxyErr error, startedAt time.Time) {
	store, supported := pinStore.(sessionpin.SiblingPreferenceStore)
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
	written, err := store.RecordSiblingPreference(context.Background(), sessionpin.SiblingPreferenceWrite{
		InstallationID: turn.InstallationID,
		SessionKey:     turn.SiblingPreferenceKey,
		Strategy:       strategyForTurnLoopResult(turn),
		Group:          group,
		Preferred:      served.Model,
		Displaced:      displaced,
		StartedAt:      startedAt,
	})
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
