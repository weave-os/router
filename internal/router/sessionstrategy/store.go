package sessionstrategy

import (
	"context"
	"errors"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
)

const SessionKeyLen = sessionpin.SessionKeyLen

var ErrInvalidStrategy = errors.New("invalid session strategy preference")

type Preference struct {
	InstallationID uuid.UUID
	SessionKey     [SessionKeyLen]byte
	Strategy       router.Strategy
}

func (p Preference) Validate() error {
	if !router.IsBetaStrategy(p.Strategy) {
		return ErrInvalidStrategy
	}
	return nil
}

type Store interface {
	Get(ctx context.Context, installationID uuid.UUID, sessionKey [SessionKeyLen]byte) (Preference, bool, error)
	Toggle(ctx context.Context, preference Preference) (bool, error)
	Disable(ctx context.Context, installationID uuid.UUID, sessionKey [SessionKeyLen]byte, strategy router.Strategy) (bool, error)
}
