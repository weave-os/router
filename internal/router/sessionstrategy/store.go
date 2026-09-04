// Package sessionstrategy defines the inner-ring contract for explicit
// per-session routing strategy preferences.
package sessionstrategy

import (
	"context"
	"errors"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
)

// SessionKeyLen is the shared sha256-truncated session key length.
const SessionKeyLen = sessionpin.SessionKeyLen

// ErrInvalidStrategy is returned when a caller tries to persist any strategy
// other than the explicit beta override. Stable routing is represented by no row.
var ErrInvalidStrategy = errors.New("invalid session strategy preference")

// Preference is the explicit strategy selected for one installation-scoped session.
type Preference struct {
	InstallationID uuid.UUID
	SessionKey     [SessionKeyLen]byte
	Strategy       router.Strategy
}

// Validate rejects values that are not valid explicit preferences.
func (p Preference) Validate() error {
	if p.Strategy != router.StrategyHMMBeta {
		return ErrInvalidStrategy
	}
	return nil
}

// Store persists explicit session strategy preferences. Get returns
// (zero, false, nil) when the session uses stable routing. Toggle and Disable
// are atomic writes so overlapping /beta commands cannot both act on the same
// prior state; each caller sees its own persisted result.
type Store interface {
	Get(ctx context.Context, installationID uuid.UUID, sessionKey [SessionKeyLen]byte) (Preference, bool, error)
	Toggle(ctx context.Context, preference Preference) (bool, error)
	Disable(ctx context.Context, installationID uuid.UUID, sessionKey [SessionKeyLen]byte) (bool, error)
}
