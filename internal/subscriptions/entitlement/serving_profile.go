package entitlement

import "weave-os/router/internal/router"

const LegacyBoostProfileKey = "32acfab2-7d79-55c7-8d63-e8535fb66027"

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

func ServingProfileFor(plan Plan) (ServingProfile, bool) {
	if !plan.Valid() {
		return ServingProfile{}, false
	}
	return servingProfiles[plan], true
}

func PlanHasServingProfile(plan Plan) bool {
	profile, ok := ServingProfileFor(plan)
	return ok && profile.Key != ""
}
