package proxy

import (
	"context"
	"log/slog"
	"time"

	"weave-os/router/internal/router/turntype"
)

type SessionTurnClock interface {
	AdvanceSessionTurnClock(ctx context.Context, advance SessionTurnClockAdvance) (previous SessionTurnClockReading, found bool, err error)
}

type SessionTurnClockAdvance struct {
	InstallationID  string
	SessionKey      []byte
	ResponseEndedAt time.Time
	ServedModel     string
}

type SessionTurnClockReading struct {
	ResponseEndedAt time.Time
	ServedModel     string
}

func (s *Service) WithSessionTurnClock(clock SessionTurnClock) *Service {
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
	previous, found, err := s.turnClock.AdvanceSessionTurnClock(clockCtx, SessionTurnClockAdvance{
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

func userPromptGap(p InsertTelemetryParams, previous SessionTurnClockReading) (*int64, string) {
	if p.UserPrompt == nil || !*p.UserPrompt || previous.ResponseEndedAt.After(p.Timestamp) {
		return nil, ""
	}
	gapMs := p.Timestamp.Sub(previous.ResponseEndedAt).Milliseconds()
	return &gapMs, previous.ServedModel
}
