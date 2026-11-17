package proxy

import (
	"context"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
)

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
			if err := s.spiralShadowStore.InsertSpiralShadowEvent(context.WithoutCancel(ctx), event); err != nil {
				log.Error("spiral-shadow: event insert failed", "err", err)
				continue
			}
		}
		s.spiralTracker.MarkFired(key)
	}
}

func turnSignalCaptureAllowed(trainingAllowed bool, capture ContentCaptureMode) bool {
	return trainingAllowed && (capture == CaptureHashed || capture == CaptureFull)
}

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

func (s *Service) WithSpiralShadowConfig(enabled bool) *Service {
	s.spiralShadowEnabled = enabled
	return s
}

func (s *Service) WithSpiralShadowStore(store conversation.SpiralShadowStore) *Service {
	s.spiralShadowStore = store
	return s
}
