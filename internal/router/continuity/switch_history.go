package continuity

import (
	"context"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

type HistoryRows struct {
	Store    sessionpin.Store
	Now      func() time.Time
	Strategy router.Strategy
}

type TurnHistoryRecord struct {
	SessionKey     [sessionpin.SessionKeyLen]byte
	InstallationID uuid.UUID
	PinRole        string
	Fresh          router.Decision
	Decision       router.Decision
	Usage          sessionpin.Usage
}

type SwitchHistory interface {
	HistoryRole(pinRole string) string
	LoadHistory(ctx context.Context, rows HistoryRows, sessionKey [sessionpin.SessionKeyLen]byte, pinRole string) sessionpin.Pin
	RecordTurnHistory(ctx context.Context, rows HistoryRows, rec TurnHistoryRecord) (role string, usageRecorded bool)
}
