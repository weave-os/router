package proxy_test

import (
	"context"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
)

// goldenRouterAtomScenarios pin the branch inputs of the router atoms
// (authoritative upgrade evidence, sliding threshold, effort hysteresis and
// usage-bypass coverage) so a move-only refactor of them stays byte-identical.
func goldenRouterAtomScenarios() []goldenScenario {
	return append(append(append(atomAuthoritativeUpgradeScenarios(), atomSlidingThresholdScenarios()...),
		atomEffortHysteresisScenarios()...), atomUsageBypassCoverageScenarios()...)
}

func atomFloat(v float64) *float64 { return &v }

func atomHMM(model, group string, confidence float32, margin *float64) router.Decision {
	d := routingPolicyHMM(model, confidence)
	d.Metadata.PolicyGroup = group
	d.Metadata.ClassifierMargin = margin
	return d
}

// atomWarmPin marks the pin's prompt cache warm and its first pin at the
// harness epoch, which the fake store leaves zero.
func atomWarmPin(p *sessionpin.Pin) {
	p.LastTurnEndedAt = goldenEpoch
	p.FirstPinnedAt = goldenEpoch
}

func atomOptions(opts ...func(*proxy.Service) *proxy.Service) []func(*proxy.Service) *proxy.Service {
	return opts
}

func atomUpgradeConfig(threshold *float64, staleAfter time.Duration) func(*proxy.Service) *proxy.Service {
	return func(s *proxy.Service) *proxy.Service {
		return s.EditHMMConfig(func(c *hmm.Config) {
			c.UpgradeShadow = hmm.UpgradeShadowConfig{MarginThreshold: threshold, StalePinAfter: staleAfter}
		})
	}
}

func atomScenario(name string, setup routingPolicySetup, requests ...goldenRequest) goldenScenario {
	sc := routingPolicyScenario(name, setup, requests...)
	sc.name = "router_atom_" + name
	return sc
}

func atomAuthoritativeUpgradeScenarios() []goldenScenario {
	authoritative := policy.Capabilities{AuthoritativePerTurnSelection: true}
	first, next := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	upgrade := func(name string, fresh router.Decision, pinGroup string, options []func(*proxy.Service) *proxy.Service, requests ...goldenRequest) goldenScenario {
		turns := routingPolicyTurns(atomHMM(routingPolicyLow, pinGroup, 0.9, nil), fresh)
		session := &routingPolicySession{}
		warmed := append([]goldenRequest{}, requests...)
		for i := 1; i < len(warmed); i++ {
			warmed[i].decorateContext = routingPolicyCtx(session.editPins("default_mid", atomWarmPin))
		}
		return atomScenario("authoritative_upgrade_"+name, routingPolicySetup{turns: turns, caps: authoritative, session: session, options: options}, warmed...)
	}
	cold := func(name string, fresh router.Decision, options []func(*proxy.Service) *proxy.Service) goldenScenario {
		return atomScenario("authoritative_upgrade_"+name, routingPolicySetup{
			turns: routingPolicyTurns(atomHMM(routingPolicyLow, "low", 0.9, nil), fresh), caps: authoritative, options: options,
		}, first, next)
	}
	calibrated := atomUpgradeConfig(atomFloat(0.5), 0)
	session := &routingPolicySession{}
	demoted := atomScenario("authoritative_upgrade_demoted_model_skipped_for_next_candidate", routingPolicySetup{
		turns: [][]router.Decision{{atomHMM(routingPolicyLow, "low", 0.9, nil)}, {atomHMM(routingPolicyHigh, "high", 0.9, atomFloat(0.9)), atomHMM(routingPolicyMid, "medium", 0.9, atomFloat(0.9))}},
		caps:  authoritative, session: session, options: atomOptions(calibrated),
	}, first, routingPolicyRequest(routingPolicyFollowUp("Now say bye"), session.editPins("default_mid", func(p *sessionpin.Pin) {
		p.DemotedModels = []string{routingPolicyHigh}
	})))
	voteSession := &routingPolicySession{}
	repeatedSameGroupVotes := atomScenario("authoritative_upgrade_repeated_same_group_votes", routingPolicySetup{
		turns: routingPolicyTurns(atomHMM(routingPolicyLow, "high", 0.9, nil), atomHMM(routingPolicyHigh, "high", 0.3, atomFloat(0.1))),
		caps:  authoritative, session: voteSession,
		options: atomOptions(calibrated, func(s *proxy.Service) *proxy.Service {
			return s.EditHMMConfig(func(c *hmm.Config) { c.UpgradeVotes = 2 })
		}),
	}, first, routingPolicyRequest(routingPolicyFollowUp("Now say bye"), voteSession.editPins("default_mid", func(p *sessionpin.Pin) {
		atomWarmPin(p)
		p.ConsecutiveUpgradeVotes = 1
	})))
	return []goldenScenario{
		demoted,
		upgrade("margin_missing", atomHMM(routingPolicyHigh, "high", 0.9, nil), "low", nil, first, next),
		upgrade("margin_out_of_range", atomHMM(routingPolicyHigh, "high", 0.9, atomFloat(1.5)), "low", atomOptions(calibrated), first, next),
		upgrade("margin_without_threshold", atomHMM(routingPolicyHigh, "high", 0.9, atomFloat(0.6)), "low", nil, first, next),
		upgrade("margin_above_threshold_group_escalation", atomHMM(routingPolicyHigh, "high", 0.3, atomFloat(0.6)), "low", atomOptions(calibrated), first, next),
		upgrade("margin_above_threshold_same_group", atomHMM(routingPolicyHigh, "high", 0.3, atomFloat(0.6)), "high", atomOptions(calibrated), first, next),
		upgrade("margin_below_threshold", atomHMM(routingPolicyHigh, "high", 0.9, atomFloat(0.1)), "low", atomOptions(calibrated), first, next),
		upgrade("warm_pin_same_group_low_margin", atomHMM(routingPolicyHigh, "high", 0.9, atomFloat(0.1)), "high", atomOptions(calibrated), first, next),
		repeatedSameGroupVotes,
		cold("cold_pin_without_prior_turn_end", atomHMM(routingPolicyHigh, "high", 0.3, atomFloat(0.1)), atomOptions(calibrated)),
		upgrade("pin_older_than_stale_age", atomHMM(routingPolicyHigh, "high", 0.3, atomFloat(0.1)), "low", atomOptions(atomUpgradeConfig(atomFloat(0.5), time.Minute)),
			first, goldenRequest{body: routingPolicyFollowUp("Now say bye"), advance: 2 * time.Minute}),
		upgrade("force_cluster_high_on_low_pin", atomHMM(routingPolicyHigh, "high", 0.3, atomFloat(0.1)), "low", atomOptions(calibrated),
			first, goldenRequest{body: routingPolicyFollowUp("Now say bye"), headers: map[string]string{proxy.ForceClusterHeader: "high"}}),
		upgrade("score_policy_with_gate_off", atomHMM(routingPolicyHigh, "high", 0.3, atomFloat(0.1)), "low",
			atomOptions(calibrated, func(s *proxy.Service) *proxy.Service {
				return s.EditHMMConfig(func(c *hmm.Config) {
					c.UpgradePolicy = flags.AuthoritativeUpgradePolicyScore
					c.UpgradeGate = false
				})
			}), first, next),
	}
}

func atomClassified(group, label string, probability float64) router.Decision {
	d := atomHMM(routingPolicyHigh, group, 0.9, atomFloat(0.1))
	d.Metadata.ClassifierPredictedLabel = label
	d.Metadata.ClassifierProbabilities = map[string]float64{label: probability}
	return d
}

func atomSlidingThresholdScenarios() []goldenScenario {
	authoritative := policy.Capabilities{AuthoritativePerTurnSelection: true}
	first, next := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	sliding := atomOptions(atomUpgradeConfig(atomFloat(0.5), 0), func(s *proxy.Service) *proxy.Service {
		return s.EditHMMConfig(func(c *hmm.Config) { c.UpgradeSlidingThreshold = true })
	})
	scenario := func(name, pinGroup string, fresh router.Decision) goldenScenario {
		return atomScenario("sliding_threshold_"+name, routingPolicySetup{
			turns: routingPolicyTurns(atomHMM(routingPolicyLow, pinGroup, 0.9, nil), fresh), caps: authoritative, options: sliding,
		}, first, next)
	}
	unscored := atomClassified("high", "high", 0.9)
	unscored.Metadata.ClassifierProbabilities = map[string]float64{"low": 0.9}
	return []goldenScenario{
		scenario("below_step_band_high_label_on_low_pin", "low", atomClassified("high", "high", 0.2)),
		scenario("fresh_group_differs_from_target", "low", atomClassified("maximum", "high", 0.6)),
		scenario("uncovered_label_pair", "high", atomClassified("high", "low", 0.9)),
		scenario("unscored_label", "low", unscored),
	}
}

func atomEffort(model, effort, arm string, scores map[string]float32) router.Decision {
	d := atomHMM(model, "high", 0.9, nil)
	d.Effort = effort
	d.Metadata.SelectedRosterArmID = arm
	d.Metadata.ArmScores = scores
	return d
}

func atomEffortHysteresisScenarios() []goldenScenario {
	first, next := goldenRequest{body: routingPolicyFirstTurn}, goldenRequest{body: routingPolicyFollowUp("Now say bye")}
	scores := func(high, low float32) map[string]float32 {
		return map[string]float32{"golden-arm:high": high, "golden-arm:low": low}
	}
	scenario := func(name string, fresh router.Decision) goldenScenario {
		return atomScenario("effort_hysteresis_"+name, routingPolicySetup{
			turns: routingPolicyTurns(atomEffort(routingPolicyHigh, "high", "golden-arm:high", scores(0.6, 0.4)), fresh),
		}, first, next)
	}
	return []goldenScenario{
		scenario("challenger_score_slightly_above_incumbent", atomEffort(routingPolicyHigh, "low", "golden-arm:low", scores(0.5, 0.51))),
		scenario("challenger_score_far_above_incumbent", atomEffort(routingPolicyHigh, "low", "golden-arm:low", scores(0, 1.5))),
		scenario("missing_incumbent_score", atomEffort(routingPolicyHigh, "low", "golden-arm:low", map[string]float32{"golden-arm:low": 0.5})),
		scenario("fresh_model_differs_from_incumbent", atomEffort(routingPolicyMid, "low", "golden-arm:low", scores(0.5, 0.51))),
	}
}

func atomUsageBypassCoverageScenarios() []goldenScenario {
	const token = "sk-ant-oat01-golden-router-atom"
	threshold := 0.8
	configure := func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		svc = routingPolicySetup{turns: routingPolicyTurns(routingPolicyHMM(routingPolicyLow, 0.9))}.configure(svc, h)
		observer := usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)
		observer.Record(observer.Key([]byte(token)), usage.Snapshot{Primary: usage.Window{UsedPercent: 0.2, WindowMinutes: 300}})
		return svc.WithUsageObserver(observer)
	}
	ctx := func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, credentials.AnthropicSubscriptionContextKey{}, token)
		ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyClaudeEnterpriseOnly: false}})
		return context.WithValue(ctx, proxy.InstallationUsageBypassContextKey{}, proxy.UsageBypassConfig{Enabled: true, Threshold: &threshold})
	}
	scenario := func(name, body string, decorators ...func(context.Context) context.Context) goldenScenario {
		return goldenScenario{name: "router_atom_usage_bypass_" + name, ingress: ingressAnthropic, upstreams: routingPolicyUpstreams(),
			requests: []goldenRequest{routingPolicyRequest(body, append([]func(context.Context) context.Context{ctx}, decorators...)...)}, configure: configure}
	}
	return []goldenScenario{
		scenario("uncatalogued_requested_model", `{"model":"golden-unknown-model","max_tokens":64,"messages":[{"role":"user","content":"Say hi"}]}`),
		scenario("non_anthropic_requested_model", `{"model":"`+routingPolicyTextOnly+`","max_tokens":64,"messages":[{"role":"user","content":"Say hi"}]}`),
		scenario("requested_model_outside_allowed_list", routingPolicyFirstTurn, routingPolicyAllowModels(routingPolicyLow)),
		scenario("requested_model_excluded", routingPolicyFirstTurn, routingPolicyExcludeModels(routingPolicyMid)),
		scenario("other_model_excluded", routingPolicyFirstTurn, routingPolicyExcludeModels(routingPolicyHigh)),
		scenario("anthropic_byok_key_present", routingPolicyFirstTurn, routingPolicyExternalKeys(byokKey(providers.ProviderAnthropic, "sk-ant-golden-atom", ""))),
	}
}
