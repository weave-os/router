package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

var pinNeverExpires = routingpolicy.PinNeverExpires

func pinExpiry(reason string) time.Time { return routingpolicy.PinExpiry(reason) }

func pinMatchesEffectiveStrategy(ctx context.Context, pin sessionpin.Pin) bool {
	return routingpolicy.PinMatchesEffectiveStrategy(ctx, pin)
}

func decisionPolicyGroup(dec router.Decision) string { return routingpolicy.DecisionPolicyGroup(dec) }

func hmmHistoryRole(role string) string { return routingpolicy.HMMHistoryRole(role) }

func isUserForcedReason(reason string) bool { return routingpolicy.IsUserForcedReason(reason) }

func forceModelHistoryRole(role string) string { return routingpolicy.ForceModelHistoryRole(role) }
