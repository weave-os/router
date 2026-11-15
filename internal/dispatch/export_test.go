package dispatch

import "time"

// SetHoldMax shortens the early-stop hold so a test can observe the timeout.
func (g *EarlyStopGate) SetHoldMax(d time.Duration) { g.holdMax = d }
