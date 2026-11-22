package proxy

import (
	"context"

	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

func loadSwitchHistory(ctx context.Context, ports continuity.StrategyPorts, pins routingpolicy.SessionPins, sessionKey [sessionpin.SessionKeyLen]byte, role string) sessionpin.Pin {
	return ports.History.LoadHistory(ctx, pins.HistoryRows(), sessionKey, role)
}
