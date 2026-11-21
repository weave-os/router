package proxy

import (
	"context"
	"log/slog"
	"time"

	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/turnrecord"
)

func (s *Service) WithSessionTurnClock(clock telemetry.SessionTurnClock) *Service {
	s.turnTelemetry.turnClock = clock
	return s
}

const sessionTurnClockTimeout = time.Second

func advancesSessionTurnClock(p turnrecord.InsertTelemetryParams) bool {
	if p.SpanType != "router.upstream" || len(p.SessionKey) == 0 || p.DecisionModel == "" || p.OutputTokens <= 0 {
		return false
	}
	return p.TurnType == string(turntype.MainLoop) || p.TurnType == string(turntype.ToolResult)
}

func (t turnTelemetry) applyUserPromptGap(ctx context.Context, log *slog.Logger, p *turnrecord.InsertTelemetryParams) {
	if t.turnClock == nil || !advancesSessionTurnClock(*p) {
		return
	}
	clockCtx, cancel := context.WithTimeout(ctx, sessionTurnClockTimeout)
	defer cancel()
	previous, found, err := t.turnClock.AdvanceSessionTurnClock(clockCtx, telemetry.SessionTurnClockAdvance{
		InstallationID:  p.InstallationID,
		SessionKey:      p.SessionKey,
		ResponseEndedAt: p.Timestamp.Add(time.Duration(p.TotalLatencyMs) * time.Millisecond),
		ServedModel:     p.DecisionModel,
	})
	if err != nil {
		log.Warn("Session turn clock advance failed", "err", err)
		return
	}
	if found {
		p.UserPromptGapMs, p.UserPromptGapPriorModel = userPromptGap(*p, previous)
	}
}

func userPromptGap(p turnrecord.InsertTelemetryParams, previous telemetry.SessionTurnClockReading) (*int64, string) {
	if p.UserPrompt == nil || !*p.UserPrompt || previous.ResponseEndedAt.After(p.Timestamp) {
		return nil, ""
	}
	gapMs := p.Timestamp.Sub(previous.ResponseEndedAt).Milliseconds()
	return &gapMs, previous.ServedModel
}
