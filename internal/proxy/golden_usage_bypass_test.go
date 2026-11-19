package proxy_test

import (
	"context"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
)

const usageBypassGoldenToken = "sk-ant-oat01-golden-usage-bypass"

// usageBypassGoldenSeat wires the subscription usage observer; utilization < 0
// leaves it cold, otherwise it records that primary-window utilization.
func usageBypassGoldenSeat(utilization float64) func(*proxy.Service, *goldenHarness) *proxy.Service {
	return func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		observer := usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)
		if utilization >= 0 {
			observer.Record(observer.Key([]byte(usageBypassGoldenToken)), usage.Snapshot{
				Primary: usage.Window{UsedPercent: utilization, WindowMinutes: 300},
			})
		}
		return svc.WithUsageObserver(observer)
	}
}

func usageBypassGoldenCtx(threshold float64) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, proxy.AnthropicSubscriptionContextKey{}, usageBypassGoldenToken)
		ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyClaudeEnterpriseOnly: false}})
		return context.WithValue(ctx, proxy.InstallationUsageBypassContextKey{}, proxy.UsageBypassConfig{Enabled: true, Threshold: &threshold})
	}
}

func usageBypassGoldenScenario(name string, utilization float64, requests ...goldenRequest) goldenScenario {
	return goldenScenario{
		name:      "routing_policy_usage_bypass_" + name,
		ingress:   ingressAnthropic,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, routingPolicyLow)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(false, "ok"), anthropicOK(false, "ok")}},
		requests:  requests,
		configure: usageBypassGoldenSeat(utilization),
	}
}

// goldenUsageBypassScenarios pin the subscription pass-through lane: below the
// installation's utilization threshold (or with no observation yet) the turn
// skips the scorer and serves the requested model on the seat; at or above it
// the turn is routed.
func goldenUsageBypassScenarios() []goldenScenario {
	request := func(threshold float64) goldenRequest {
		return goldenRequest{body: routingPolicyFirstTurn, decorateContext: usageBypassGoldenCtx(threshold)}
	}
	return []goldenScenario{
		usageBypassGoldenScenario("below_threshold_serves_requested_model", 0.2, request(0.8)),
		usageBypassGoldenScenario("cold_observer_serves_requested_model", -1, request(0.8)),
		usageBypassGoldenScenario("at_threshold_routes", 0.9, request(0.8)),
		usageBypassGoldenScenario("follow_up_below_threshold_skips_session_pin", 0.2,
			goldenRequest{body: routingPolicyFirstTurn},
			goldenRequest{body: routingPolicyFollowUp("Now say bye"), decorateContext: usageBypassGoldenCtx(0.8)}),
	}
}
