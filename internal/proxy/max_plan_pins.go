package proxy

import (
	"context"
	"os"

	"weave-os/router/internal/subscriptions/entitlement"
)

// MaxPlanRosterPinsEnabledEnv gates the Max plan's hardcoded cluster pins. The
// pins are a stop-gap that serves the open-weight Max roster without
// publishing it to the lane the legacy router reads; the lane is the permanent
// home, and turning this off restores plain roster selection with no deploy.
const MaxPlanRosterPinsEnabledEnv = "ROUTER_MAX_PLAN_ROSTER_PINS"

// maxPlanClusterPins is the open-weight Max roster expressed as per-cluster
// catalog IDs. Cluster labels are the served policy's classifier groups; a
// label the live artifact does not report contributes nothing, and a pinned
// model that is excluded, undeployed, or unprovidered falls open to ordinary
// selection rather than failing the turn.
var maxPlanClusterPins = map[string][]string{
	"low":     {"deepseek/deepseek-v4.1-flash", "xiaomi/mimo-v2.6-flash"},
	"medium":  {"xiaomi/mimo-v2.6-flash", "z-ai/glm-5.3-flash"},
	"high":    {"deepseek/deepseek-v4.1-flash", "z-ai/glm-5.3-flash"},
	"maximum": {"xiaomi/mimo-v2.6-pro", "deepseek/deepseek-v4.1-flash"},
}

// maxPlanRosterPins returns the Max roster pins for a Max subscriber, or nil
// for every other caller. Max's product boundary already admits open-source
// models only, so the pins narrow that boundary to one roster rather than
// widening what the plan sells.
func maxPlanRosterPins(ctx context.Context) map[string][]string {
	if os.Getenv(MaxPlanRosterPinsEnabledEnv) != "true" {
		return nil
	}
	plan, subscribed := entitlement.ProductScopeFromContext(ctx)
	if !subscribed || plan != entitlement.PlanMax {
		return nil
	}
	pins := make(map[string][]string, len(maxPlanClusterPins))
	for cluster, catalogIDs := range maxPlanClusterPins {
		pins[cluster] = append([]string(nil), catalogIDs...)
	}
	return pins
}
