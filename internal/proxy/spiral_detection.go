package proxy

import (
	"context"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
)

// handleSpiralShadow records one durable event + one log line per
// (session, role, reason). Takes no routing action. Gated upstream by
// spiralShadowEnabled and the turn-type guard.
func (s *Service) handleSpiralShadow(
	ctx context.Context,
	sig conversation.SpiralSignals,
	reasons []conversation.SpiralReason,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	routedModel string,
	turnType string,
	trainingAllowed bool,
	capture ContentCaptureMode,
) {
	if !turnSignalCaptureAllowed(trainingAllowed, capture) {
		return
	}
	log := observability.FromContext(ctx)
	for _, reason := range reasons {
		reasonText := string(reason)
		key := conversation.SpiralFiredKey(sessionKey, role, reasonText)
		if s.spiralTracker.Fired(key) {
			continue
		}

		// Best-effort: a lookup failure proceeds — an extra row beats a
		// lost one in shadow mode.
		if s.spiralShadowStore != nil && installationID != uuid.Nil {
			count, err := s.spiralShadowStore.CountSpiralShadowEvents(ctx, sessionKey[:], role, reasonText)
			if err != nil {
				log.Error("spiral-shadow: budget lookup failed", "err", err)
			} else if count > 0 {
				s.spiralTracker.MarkFired(key)
				continue
			}
		}

		log.Info("router.spiral_shadow",
			"reason", reason,
			"routed_model", routedModel,
			"turn_type", turnType,
			"err_streak", sig.ErrStats.TrailingErrStreak,
			"errored_results", sig.ErrStats.Errored,
			"tool_results", sig.ErrStats.Total,
			"max_same_file_edits", sig.MaxSameFileEdits,
			"same_file_path_hash", sig.SameFilePathHash,
			"repeat_frac", sig.RepeatFrac,
			"monologue_len", sig.MonologueLen,
			"tool_call_count", sig.ToolCallCount,
			"message_count", sig.MessageCount,
			"ping_pong_len", sig.PingPongLen,
			"steps_since_progress", sig.StepsSinceProgress,
			"session_key_prefix", shortSessionKey(sessionKey),
			"role", role,
		)

		if s.spiralShadowStore != nil && installationID != uuid.Nil {
			event := conversation.SpiralShadowEvent{
				InstallationID:     installationID.String(),
				SessionKey:         sessionKey[:],
				Role:               role,
				RoutedModel:        routedModel,
				TurnType:           turnType,
				Reason:             reasonText,
				ErrStreak:          int32(sig.ErrStats.TrailingErrStreak),
				ErroredResults:     int32(sig.ErrStats.Errored),
				ToolResults:        int32(sig.ErrStats.Total),
				MaxSameFileEdits:   int32(sig.MaxSameFileEdits),
				SameFilePathHash:   sig.SameFilePathHash,
				RepeatFrac:         sig.RepeatFrac,
				MonologueLen:       int32(sig.MonologueLen),
				ToolCallCount:      int32(sig.ToolCallCount),
				MessageCount:       int32(sig.MessageCount),
				PingPongLen:        int32(sig.PingPongLen),
				StepsSinceProgress: int32(sig.StepsSinceProgress),
			}
			// context.WithoutCancel: the request ctx may already be canceled;
			// losing the row would skew the shadow fire-rate corpus.
			if err := s.spiralShadowStore.InsertSpiralShadowEvent(context.WithoutCancel(ctx), event); err != nil {
				log.Error("spiral-shadow: event insert failed", "err", err)
				continue // leave the LRU unset so the next turn retries
			}
		}
		s.spiralTracker.MarkFired(key)
	}
}

// turnSignalCaptureAllowed requires both privacy gates. CaptureOff is the
// fallback for an unknown mode, so both gates fail closed by default.
func turnSignalCaptureAllowed(trainingAllowed bool, capture ContentCaptureMode) bool {
	return trainingAllowed && (capture == CaptureHashed || capture == CaptureFull)
}

// applyTurnSignalTelemetry fills p only when all gates pass, preserving
// the distinction between a measured zero and an absent snapshot.
func applyTurnSignalTelemetry(
	p *InsertTelemetryParams,
	sig conversation.SpiralSignals,
	reasons []conversation.SpiralReason,
	computed bool,
	enabled bool,
	trainingAllowed bool,
	capture ContentCaptureMode,
) {
	if p == nil || !computed || !enabled || !turnSignalCaptureAllowed(trainingAllowed, capture) {
		return
	}
	p.SpiralErrStreak = int32Ptr(int32(sig.ErrStats.TrailingErrStreak))
	p.SpiralErroredResults = int32Ptr(int32(sig.ErrStats.Errored))
	p.SpiralToolResults = int32Ptr(int32(sig.ErrStats.Total))
	p.SpiralMaxSameFileEdits = int32Ptr(int32(sig.MaxSameFileEdits))
	p.SpiralSameFilePathHash = sig.SameFilePathHash
	repeatFrac := sig.RepeatFrac
	p.SpiralRepeatFrac = &repeatFrac
	p.SpiralMonologueLen = int32Ptr(int32(sig.MonologueLen))
	p.SpiralToolCallCount = int32Ptr(int32(sig.ToolCallCount))
	p.SpiralMessageCount = int32Ptr(int32(sig.MessageCount))
	p.SpiralPingPongLen = int32Ptr(int32(sig.PingPongLen))
	p.SpiralStepsSinceProgress = int32Ptr(int32(sig.StepsSinceProgress))
	editAttempted := sig.EditAttempted
	p.SpiralEditAttempted = &editAttempted
	if reasons == nil {
		reasons = []conversation.SpiralReason{}
	}
	p.SpiralReasons = conversation.SpiralReasonStrings(reasons)
}

// WithSpiralShadowConfig sets the shadow-mode spiral detector kill switch.
// enabled=false skips signal computation entirely. Shadow mode takes no
// routing action either way; the switch exists to shed the per-turn scan
// cost if it ever misbehaves.
func (s *Service) WithSpiralShadowConfig(enabled bool) *Service {
	s.spiralShadowEnabled = enabled
	return s
}

// WithSpiralShadowStore wires the durable sink for shadow spiral events
// (router.spiral_shadow_events). Nil degrades to log-only fires with
// replica-local de-duplication.
func (s *Service) WithSpiralShadowStore(store conversation.SpiralShadowStore) *Service {
	s.spiralShadowStore = store
	return s
}
