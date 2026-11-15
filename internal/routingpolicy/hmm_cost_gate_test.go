package routingpolicy_test

import (
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

const stayModel = "claude-haiku-4-5"

func hmmGate() routingpolicy.HMMCostGate {
	return routingpolicy.HMMCostGate{
		RegisteredProviders: func() map[string]struct{} {
			return map[string]struct{}{providers.ProviderAnthropic: {}}
		},
	}
}

func hmmPin(reason string, endedAgo time.Duration) sessionpin.Pin {
	return sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           stayModel,
		Reason:          reason,
		LastTurnEndedAt: time.Now().Add(-endedAgo),
		PinnedUntil:     time.Now().Add(time.Hour),
	}
}

func TestHMMCostGateDecideWithoutStayPinSwitches(t *testing.T) {
	fresh := router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-5"}
	stay, plan, sticky := hmmGate().Decide(router.Request{}, sessionpin.Pin{}, sessionpin.Pin{}, fresh, 1000, false)
	if sticky || stay.Model != "" {
		t.Fatalf("Decide() = %+v sticky=%v, want no stay pin", stay, sticky)
	}
	if plan.Outcome != planner.OutcomeSwitch || plan.Reason != planner.ReasonNoPin {
		t.Fatalf("plan = %+v, want switch/no-pin", plan)
	}
}

func TestHMMCostGateStayPinIgnoresNonHMMActivePin(t *testing.T) {
	if _, ok := hmmGate().StayPin(router.Request{}, hmmPin("cluster_scorer", time.Minute), sessionpin.Pin{}); ok {
		t.Fatal("StayPin accepted an active pin not written by HMM")
	}
	got, ok := hmmGate().StayPin(router.Request{}, hmmPin("hmm_policy", time.Minute), sessionpin.Pin{})
	if !ok || got.Model != stayModel {
		t.Fatalf("StayPin() = %+v, %v; want %s", got, ok, stayModel)
	}
}

func TestHMMCostGateStayPinPrefersMostRecentTurn(t *testing.T) {
	older := hmmPin("hmm_policy", time.Hour)
	newer := hmmPin(routingpolicy.HMMHistoryReason, time.Minute)
	newer.Model = "claude-sonnet-4-5"
	got, ok := hmmGate().StayPin(router.Request{}, older, newer)
	if !ok || got.Model != newer.Model {
		t.Fatalf("StayPin() = %+v, %v; want the history pin %s", got, ok, newer.Model)
	}
}

func TestHMMCostGateNormalizeStayPinRejectsIneligibleModels(t *testing.T) {
	pin := hmmPin("hmm_policy", time.Minute)
	cases := map[string]struct {
		gate routingpolicy.HMMCostGate
		req  router.Request
	}{
		"excluded":      {hmmGate(), router.Request{ExcludedModels: map[string]struct{}{stayModel: {}}}},
		"auto-disabled": {hmmGate(), router.Request{AutomaticExcludedModels: map[string]struct{}{stayModel: {}}}},
		"unavailable": {func() routingpolicy.HMMCostGate {
			g := hmmGate()
			g.AvailableModels = map[string]struct{}{"claude-sonnet-4-5": {}}
			return g
		}(), router.Request{}},
		"provider not enabled": {hmmGate(), router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got, ok := tc.gate.NormalizeStayPin(tc.req, pin); ok {
				t.Fatalf("NormalizeStayPin() = %+v, want rejection", got)
			}
		})
	}
	if _, ok := hmmGate().NormalizeStayPin(router.Request{}, pin); !ok {
		t.Fatal("NormalizeStayPin rejected an eligible pin")
	}
}

func TestHMMCostGateNormalizeStayPinRejectsStalePins(t *testing.T) {
	expired := hmmPin("hmm_policy", time.Minute)
	expired.PinnedUntil = time.Now().Add(-time.Second)
	noTurn := hmmPin("hmm_policy", 0)
	noTurn.LastTurnEndedAt = time.Time{}
	maxedOut := hmmPin("hmm_policy", time.Minute)
	maxedOut.LastOutputLimitAt = maxedOut.LastTurnEndedAt
	for name, pin := range map[string]sessionpin.Pin{"expired": expired, "no prior turn": noTurn, "maxed out": maxedOut} {
		t.Run(name, func(t *testing.T) {
			if _, ok := hmmGate().NormalizeStayPin(router.Request{}, pin); ok {
				t.Fatal("NormalizeStayPin accepted a stale pin")
			}
		})
	}
}

func TestBaseModelOfStripsEffort(t *testing.T) {
	if got := routingpolicy.BaseModelOf("gpt-5.5:high"); got != "gpt-5.5" {
		t.Fatalf("BaseModelOf = %q", got)
	}
	if got := routingpolicy.BaseModelOf(stayModel); got != stayModel {
		t.Fatalf("BaseModelOf = %q", got)
	}
}
