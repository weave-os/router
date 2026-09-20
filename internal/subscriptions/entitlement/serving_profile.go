package entitlement

import "weave-os/router/internal/router"

// ServingProfile identifies the immutable server-owned routing profile for a plan.
type ServingProfile struct {
	Name     string
	Key      string
	Strategy router.Strategy
}

var servingProfiles = map[Plan]ServingProfile{
	PlanMax: {
		Name:     "max-default",
		Key:      "75018458-5306-5d29-9911-ec06c5376a9f",
		Strategy: router.StrategyHMM,
	},
	PlanBoost: {
		Name:     "boost-default",
		Key:      "32acfab2-7d79-55c7-8d63-e8535fb66027",
		Strategy: router.StrategyHMM,
	},
}

// ServingProfileFor returns the server-owned profile for a recognized plan.
func ServingProfileFor(plan Plan) (ServingProfile, bool) {
	profile, ok := servingProfiles[plan]
	return profile, ok
}
