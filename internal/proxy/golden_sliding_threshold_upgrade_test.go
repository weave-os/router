package proxy_test

import (
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

// goldenSlidingThresholdUpgradeScenarios pin the sliding-threshold paths that
// serve the fresh model with pin_tier=authoritative_sliding_threshold_upgrade.
func goldenSlidingThresholdUpgradeScenarios() []goldenScenario {
	firstTurn, followUp := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	marginThreshold := 0.5
	options := []func(*proxy.Service) *proxy.Service{
		func(s *proxy.Service) *proxy.Service {
			return s.WithAuthoritativeUpgradeConfig(proxy.AuthoritativeUpgradeConfig{MarginThreshold: &marginThreshold})
		},
		func(s *proxy.Service) *proxy.Service { return s.WithAuthoritativeUpgradeSlidingThreshold(true) },
	}
	scenario := func(name, pinGroup string, fresh router.Decision) goldenScenario {
		pinned := slidingUpgradeHMM(routingPolicyLow, pinGroup)
		sc := routingPolicyScenario(name, routingPolicySetup{
			turns: routingPolicyTurns(pinned, fresh), caps: policy.Capabilities{AuthoritativePerTurnSelection: true}, options: options,
		}, firstTurn, followUp)
		sc.name = "router_atom_sliding_threshold_" + name
		return sc
	}
	return []goldenScenario{
		scenario("full_band_high_label_on_low_pin", "low", slidingUpgradeClassified(routingPolicyHigh, "high", "high", 0.6)),
		scenario("step_band_high_label_on_low_pin", "low", slidingUpgradeClassified(routingPolicyMid, "medium", "high", 0.35)),
		scenario("high_label_on_medium_pin", "medium", slidingUpgradeClassified(routingPolicyHigh, "high", "high", 0.45)),
		scenario("maximum_label_on_high_pin", "high", slidingUpgradeClassified(routingPolicyHigh, "maximum", "maximum", 0.7)),
	}
}

func slidingUpgradeHMM(model, group string) router.Decision {
	d := routingPolicyHMM(model, 0.9)
	d.Metadata.PolicyGroup = group
	return d
}

func slidingUpgradeClassified(model, group, label string, probability float64) router.Decision {
	d := slidingUpgradeHMM(model, group)
	margin := 0.1
	d.Metadata.ClassifierMargin = &margin
	d.Metadata.ClassifierPredictedLabel = label
	d.Metadata.ClassifierProbabilities = map[string]float64{label: probability}
	return d
}
