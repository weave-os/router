package proxy

import (
	"github.com/google/uuid"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

type sessionTarget struct {
	strategy       router.Strategy
	installationID uuid.UUID
	sessionKey     [sessionpin.SessionKeyLen]byte
}
