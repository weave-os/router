package conversation

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type LoopBreakPins interface {
	Load(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool)
	ExpireWithHistory(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte, role, reason string) error
}

func ShortSessionKey(sessionKey [sessionpin.SessionKeyLen]byte) string {
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ""
	}
	return fmt.Sprintf("%x", sessionKey[:8])
}

type LoopBreak struct {
	Pins             LoopBreakPins
	InstallationID   uuid.UUID
	SessionKey       [sessionpin.SessionKeyLen]byte
	Role             string
	DecisionModel    string
	DecisionProvider string
}

func (b LoopBreak) NoProgress(ctx context.Context, env *translate.RequestEnvelope, count int, decisionReason ...string) string {
	pins, installationID, sessionKey, role, decisionModel, decisionProvider := b.Pins, b.InstallationID, b.SessionKey, b.Role, b.DecisionModel, b.DecisionProvider
	log := observability.FromContext(ctx)
	preserveForcedPin := len(decisionReason) > 0 && routingpolicy.IsUserForcedReason(decisionReason[0])
	if !preserveForcedPin && pins != nil && installationID != uuid.Nil && sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		pin, found := pins.Load(ctx, sessionKey, role)
		preserveForcedPin = found && routingpolicy.IsUserForcedReason(pin.Reason)
	}
	pinAction := "clearing the session pin so the next message re-routes"
	if preserveForcedPin {
		pinAction = "preserving the explicit force-model pin for the next message"
	}

	msg := fmt.Sprintf(
		"✦ **Weave Router** → No-progress loop detected: %d consecutive requests under this session routed to `%s` (`%s`) with no observable progress in %s. Stopping this turn and %s.\n\nIf the task is genuinely incomplete, send a follow-up message describing what's still needed; the router will pick a different model.\n\n",
		count, decisionModel, decisionProvider, NoProgressTimeWindow, pinAction,
	)
	if env.SourceFormat() == translate.FormatOpenAI {
		msg = fmt.Sprintf(
			"Weave Router: no-progress loop detected (%d consecutive routes to %s/%s with no progress in %s). Stopping and %s.",
			count, decisionModel, decisionProvider, NoProgressTimeWindow, pinAction,
		)
	}

	log.Info("No-progress loop detected; breaking turn",
		"repeat_count", count,
		"window_size", NoProgressWindowSize,
		"time_window", NoProgressTimeWindow.String(),
		"decision_model", decisionModel,
		"decision_provider", decisionProvider,
		"session_key_prefix", ShortSessionKey(sessionKey),
		"role", role,
	)

	if pins != nil && installationID != uuid.Nil && sessionKey != ([sessionpin.SessionKeyLen]byte{}) && !preserveForcedPin {
		if err := pins.ExpireWithHistory(ctx, installationID, sessionKey, role, "no_progress_loop_break"); err != nil {
			log.Error("no-progress-break: pin store upsert failed", "err", err)
		}
	}

	return msg
}

func (b LoopBreak) TextRepetition(ctx context.Context, env *translate.RequestEnvelope, count int, sampleHash string) string {
	pins, installationID, sessionKey, role, decisionModel, decisionProvider := b.Pins, b.InstallationID, b.SessionKey, b.Role, b.DecisionModel, b.DecisionProvider
	log := observability.FromContext(ctx)

	msg := fmt.Sprintf(
		"✦ **Weave Router** → Repetition loop detected: `%s` (`%s`) repeated the same response %d times without making progress. Stopping this turn and clearing the session pin so the next message re-routes to a fresh model.\n\nIf the task is genuinely incomplete, send a follow-up message describing what's still needed; the router will pick a different model.\n\n",
		decisionModel, decisionProvider, count,
	)
	if env.SourceFormat() == translate.FormatOpenAI {
		msg = fmt.Sprintf(
			"Weave Router: repetition loop detected (%s/%s repeated the same response %d times). Stopping and clearing the session pin; send a follow-up to resume on a different model.",
			decisionModel, decisionProvider, count,
		)
	}

	log.Info("Text-repetition loop detected; breaking turn",
		"repeat_count", count,
		"window_size", TextRepetitionWindow,
		"text_sample_hash", sampleHash,
		"decision_model", decisionModel,
		"decision_provider", decisionProvider,
		"session_key_prefix", ShortSessionKey(sessionKey),
		"role", role,
	)

	if pins != nil && installationID != uuid.Nil && sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		if err := pins.ExpireWithHistory(ctx, installationID, sessionKey, role, "text_repetition_break"); err != nil {
			log.Error("text-repetition-break: pin store upsert failed", "err", err)
		}
	}

	return msg
}
