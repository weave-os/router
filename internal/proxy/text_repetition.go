package proxy

import (
	"context"
	"fmt"
	"net/http"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/conversation"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

func (s *Service) handleTextRepetitionBreak(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	count int,
	sampleHash string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	decisionModel string,
	decisionProvider string,
	inputTokens int,
) error {
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

	if _, active := auth.BlindExperimentFrom(ctx); active {
		msg = blindExperimentLoopStopMessage
	}

	log.Info("Text-repetition loop detected; breaking turn",
		"repeat_count", count,
		"window_size", conversation.TextRepetitionWindow,
		"text_sample_hash", sampleHash,
		"decision_model", decisionModel,
		"decision_provider", decisionProvider,
		"session_key_prefix", shortSessionKey(sessionKey),
		"role", role,
	)

	if s.pinStore != nil && installationID != uuid.Nil && sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		if err := s.expireSessionPinAndHMMHistory(ctx, installationID, sessionKey, role, "text_repetition_break"); err != nil {
			log.Error("text-repetition-break: pin store upsert failed", "err", err)
		}
	}

	ack := newCommandAcknowledgment(translate.CommandLoopStop)
	return writeCommandOutcome(ctx, w, env, msg, ack.outcome, inputTokens)
}

func (s *Service) WithTextRepetitionBreak(enabled bool) *Service {
	s.textRepetitionBreakEnabled = enabled
	return s
}
