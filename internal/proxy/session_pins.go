package proxy

import (
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

var pinNeverExpires = routingpolicy.PinNeverExpires

func pinExpiry(reason string, now time.Time) time.Time { return routingpolicy.PinExpiry(reason, now) }

func pinMatchesEffectiveStrategy(strategy router.Strategy, pin sessionpin.Pin) bool {
	return routingpolicy.PinMatchesEffectiveStrategy(strategy, pin)
}

func decisionPolicyGroup(dec router.Decision) string { return routingpolicy.DecisionPolicyGroup(dec) }

func isUserForcedReason(reason string) bool { return routingpolicy.IsUserForcedReason(reason) }

const userUnforcedReason = routingpolicy.ReasonUserUnforced

func forceModelHistoryRole(role string) string { return routingpolicy.ForceModelHistoryRole(role) }
