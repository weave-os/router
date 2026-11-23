package routingpolicy

import (
	"github.com/google/uuid"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

type SessionTarget struct {
	Strategy       router.Strategy
	InstallationID uuid.UUID
	SessionKey     [sessionpin.SessionKeyLen]byte
}
