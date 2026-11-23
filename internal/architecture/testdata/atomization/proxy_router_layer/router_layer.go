package proxyrouterlayer

import (
	"context"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/sessionpin"
)

func sequence(ctx context.Context, ports continuity.StrategyPorts, helper *floor.Helper, req router.Request, fresh router.Decision, pin sessionpin.Pin) {
	turn := continuity.Turn{}
	cfg := continuity.DefaultConfig()
	_ = continuity.ToolResultSticky(req.Session, turn, cfg)
	_ = continuity.CanReusePin(req.Session, turn, cfg)
	escalated := helper.Begin(ctx, floor.Facts{}, req)
	escalated.Apply(&req)
	_ = floor.RoutingApplied(fresh)
	_ = ports.Upgrade.EffortHysteresisHold(fresh, "", "", "")
	_, _ = ports.CostGate.StayPin(continuity.CostGateConfig{}, req, pin, pin, time.Time{})
	_ = ports.CostGate.HistoryReason()
	_ = ports.Ownership.Owns("")
	_ = ports.Facts.PinStateOf(pin)
	_ = escalated.PinTier()
	_, _, _ = helper.Settle(ctx, escalated, fresh, "", nil)
	_ = helper.SettleLLM(ctx, escalated, fresh, nil)
}
