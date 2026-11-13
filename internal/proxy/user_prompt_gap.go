package proxy

import (
	"context"
	"log/slog"
	"time"

	"weave-os/router/internal/router/turntype"
)

// SessionTurnClock records when each session's main thread last finished a
// response, so the next typed prompt can be timed against it.
type SessionTurnClock interface {
	// AdvanceSessionTurnClock records a finished response and returns the
	// finish recorded before it; found is false on a session's first response.
	AdvanceSessionTurnClock(ctx context.Context, advance SessionTurnClockAdvance) (previous SessionTurnClockReading, found bool, err error)
}

// SessionTurnClockAdvance is one finished main-thread response.
type SessionTurnClockAdvance struct {
	InstallationID  string
	SessionKey      []byte
	ResponseEndedAt time.Time
	ServedModel     string
}

// SessionTurnClockReading is the most recent finished response of a session.
type SessionTurnClockReading struct {
	ResponseEndedAt time.Time
	ServedModel     string
}

// WithSessionTurnClock wires the store that times user prompts. A nil clock
// leaves the user_prompt_gap columns NULL.
func (s *Service) WithSessionTurnClock(clock SessionTurnClock) *Service {
	s.turnClock = clock
	return s
}

// sessionTurnClockTimeout bounds the clock advance so the telemetry insert that
// follows it in the same fireTelemetry budget always keeps time to run.
const sessionTurnClockTimeout = time.Second

// advancesSessionTurnClock reports whether a row is a main-thread response:
// the only turns a person reads before typing. Classifier, title, probe,
// recap, compaction, and sub-agent calls run beside the conversation, and a
// turn that failed before producing output left nothing to read.
func advancesSessionTurnClock(p InsertTelemetryParams) bool {
	if p.SpanType != "router.upstream" || len(p.SessionKey) == 0 || p.DecisionModel == "" || p.OutputTokens <= 0 {
		return false
	}
	return p.TurnType == string(turntype.MainLoop) || p.TurnType == string(turntype.ToolResult)
}

// applyUserPromptGap advances the session turn clock with this response and,
// on a typed prompt, stamps how long after the previous response it arrived.
// Runs inside fireTelemetry's goroutine, off the request path.
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

// userPromptGap is how long the user took to send this prompt after the
// previous response finished. A previous response that finished after this
// prompt arrived overlapped it, so no gap is attributable.
func userPromptGap(p InsertTelemetryParams, previous SessionTurnClockReading) (*int64, string) {
	if p.UserPrompt == nil || !*p.UserPrompt || previous.ResponseEndedAt.After(p.Timestamp) {
		return nil, ""
	}
	gapMs := p.Timestamp.Sub(previous.ResponseEndedAt).Milliseconds()
	return &gapMs, previous.ServedModel
}
