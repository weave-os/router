package proxy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/conversation"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

func shortSessionKey(sessionKey [sessionpin.SessionKeyLen]byte) string {
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ""
	}
	return fmt.Sprintf("%x", sessionKey[:8])
}

func (s *Service) handleNoProgressBreak(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	count int,
	target sessionTarget,
	role string,
	decisionModel string,
	decisionProvider string,
	inputTokens int,
	decisionReason ...string,
) error {
	log := observability.FromContext(ctx)
	preserveForcedPin := len(decisionReason) > 0 && isUserForcedReason(decisionReason[0])
	if !preserveForcedPin && s.pinStore != nil && target.installationID != uuid.Nil && target.sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		pin, found := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, target.strategy).Load(ctx, target.sessionKey, role)
		preserveForcedPin = found && isUserForcedReason(pin.Reason)
	}
	pinAction := "clearing the session pin so the next message re-routes"
	if preserveForcedPin {
		pinAction = "preserving the explicit force-model pin for the next message"
	}

	msg := fmt.Sprintf(
		"✦ **Weave Router** → No-progress loop detected: %d consecutive requests under this session routed to `%s` (`%s`) with no observable progress in %s. Stopping this turn and %s.\n\nIf the task is genuinely incomplete, send a follow-up message describing what's still needed; the router will pick a different model.\n\n",
		count, decisionModel, decisionProvider, conversation.NoProgressTimeWindow, pinAction,
	)
	if env.SourceFormat() == translate.FormatOpenAI {
		msg = fmt.Sprintf(
			"Weave Router: no-progress loop detected (%d consecutive routes to %s/%s with no progress in %s). Stopping and %s.",
			count, decisionModel, decisionProvider, conversation.NoProgressTimeWindow, pinAction,
		)
	}

	if _, active := auth.BlindExperimentFrom(ctx); active {
		msg = blindExperimentLoopStopMessage
	}

	log.Info("No-progress loop detected; breaking turn",
		"repeat_count", count,
		"window_size", conversation.NoProgressWindowSize,
		"time_window", conversation.NoProgressTimeWindow.String(),
		"decision_model", decisionModel,
		"decision_provider", decisionProvider,
		"session_key_prefix", shortSessionKey(target.sessionKey),
		"role", role,
	)

	if s.pinStore != nil && target.installationID != uuid.Nil && target.sessionKey != ([sessionpin.SessionKeyLen]byte{}) && !preserveForcedPin {
		if err := s.expireSessionPinAndHMMHistory(ctx, target.strategy, target.installationID, target.sessionKey, role, "no_progress_loop_break"); err != nil {
			log.Error("no-progress-break: pin store upsert failed", "err", err)
		}
	}

	ack := newCommandAcknowledgment(translate.CommandLoopStop)
	return writeCommandOutcome(ctx, w, env, msg, ack.outcome, inputTokens)
}
