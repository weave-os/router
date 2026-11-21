package proxy

import (
	"context"
	"log/slog"
	"time"

	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
)

func (s *Service) WithSessionTurnClock(clock telemetry.SessionTurnClock) *Service {
	s.turnClock = clock
	return s
}

const sessionTurnClockTimeout = time.Second

func advancesSessionTurnClock(p InsertTelemetryParams) bool {
	if p.SpanType != "router.upstream" || len(p.SessionKey) == 0 || p.DecisionModel == "" || p.OutputTokens <= 0 {
		return false
	}
	return p.TurnType == string(turntype.MainLoop) || p.TurnType == string(turntype.ToolResult)
}

func (s *Service) applyUserPromptGap(ctx context.Context, log *slog.Logger, p *InsertTelemetryParams) {
	if s.turnClock == nil || !advancesSessionTurnClock(*p) {
		return
	}
	clockCtx, cancel := context.WithTimeout(ctx, sessionTurnClockTimeout)
	defer cancel()
	previous, found, err := s.turnClock.AdvanceSessionTurnClock(clockCtx, telemetry.SessionTurnClockAdvance{
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

func userPromptGap(p InsertTelemetryParams, previous telemetry.SessionTurnClockReading) (*int64, string) {
	if p.UserPrompt == nil || !*p.UserPrompt || previous.ResponseEndedAt.After(p.Timestamp) {
		return nil, ""
	}
	gapMs := p.Timestamp.Sub(previous.ResponseEndedAt).Milliseconds()
	return &gapMs, previous.ServedModel
}
