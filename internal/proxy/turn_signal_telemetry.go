package proxy

import (
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/telemetry/capture"
	"weave-os/router/internal/telemetry/turnrecord"
)

func turnSignalCaptureAllowed(trainingAllowed bool, mode capture.Mode) bool {
	return trainingAllowed && (mode == capture.Hashed || mode == capture.Full)
}

func applyTurnSignalTelemetry(
	p *turnrecord.InsertTelemetryParams,
	sig conversation.SpiralSignals,
	reasons []conversation.SpiralReason,
	computed bool,
	trainingAllowed bool,
	mode capture.Mode,
) {
	if p == nil || !computed || !turnSignalCaptureAllowed(trainingAllowed, mode) {
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
