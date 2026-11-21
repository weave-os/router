package telemetry

import (
	"context"
	"time"
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
