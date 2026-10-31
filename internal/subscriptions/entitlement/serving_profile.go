package entitlement

import "weave-os/router/internal/router"

// LegacyBoostProfileKey is accepted only when reading old installation
// assignments; new Boost traffic always uses the target's default policy.
const LegacyBoostProfileKey = "32acfab2-7d79-55c7-8d63-e8535fb66027"

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
}

// ServingProfileFor returns a plan-specific routing profile when the plan owns
// one. Recognized plans without a profile use the target's default policy.
func ServingProfileFor(plan Plan) (ServingProfile, bool) {
	if !plan.Valid() {
		return ServingProfile{}, false
	}
	return servingProfiles[plan], true
}

// PlanHasServingProfile reports whether the plan has a policy separate from
// the target's default. Plan-specific routing restrictions follow this policy
// ownership, not subscription status.
func PlanHasServingProfile(plan Plan) bool {
	profile, ok := ServingProfileFor(plan)
	return ok && profile.Key != ""
}
