package proxy

import (
	"weave-os/router/internal/conversation"
)

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
