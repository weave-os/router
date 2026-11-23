package proxy

import (
	"github.com/google/uuid"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

type sessionTarget struct {
	Strategy       router.Strategy
	InstallationID uuid.UUID
	SessionKey     [sessionpin.SessionKeyLen]byte
}
