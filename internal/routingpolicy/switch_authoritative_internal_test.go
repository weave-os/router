package routingpolicy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
)

type fixedCostGate struct {
	stay   sessionpin.Pin
	served continuity.CostGateServe
}

func (g fixedCostGate) Decide(continuity.CostGateConfig, continuity.CostGateInput) (sessionpin.Pin, planner.Decision, bool) {
	return g.stay, g.served.Planner, g.served.Sticky
}

func (g fixedCostGate) StayPin(continuity.CostGateConfig, router.Request, sessionpin.Pin, sessionpin.Pin, time.Time) (sessionpin.Pin, bool) {
	return g.stay, g.stay.Model != ""
}

func (g fixedCostGate) Serve(continuity.CostGateConfig, continuity.CostGateInput, string) continuity.CostGateServe {
	return g.served
}

func (fixedCostGate) HistoryReason() string { return "hmm_history" }

type roleOnlyHistory struct{ continuity.SwitchHistory }

type pinStateOnlyFacts struct{ continuity.DecisionFacts }

func (pinStateOnlyFacts) PinStateOf(sessionpin.Pin) continuity.PinState { return continuity.PinState{} }

func (roleOnlyHistory) HistoryRole(pinRole string) string { return "history:" + pinRole }

func switchAuthoritativeContext(on bool) context.Context {
	return flags.WithOverrides(context.Background(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeySwitchDecideAuthoritative: on}})
}

func TestServeCostGateServesTheDecideSwitchVerdictOnlyWhenTheBenchmarkFlagIsOn(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	warmOpus := sessionpin.Pin{
		Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic, LastServedModel: "claude-opus-4-8", LastTurnEndedAt: now.Add(-20 * time.Second),
		LastInputTokens: 1_000, LastCachedReadTokens: 399_000, Reason: "hmm_history",
	}
	coldHaiku := sessionpin.Pin{
		Model: "claude-haiku-5-5", Provider: providers.ProviderAnthropic, LastServedModel: "claude-haiku-5-5", LastTurnEndedAt: now.Add(-3 * time.Hour), Reason: "hmm_history",
	}
	sonnet := router.Decision{Model: "claude-sonnet-4-6", Provider: providers.ProviderAnthropic, Reason: "hmm_policy"}
	opus := router.Decision{Model: "claude-opus-5-5", Provider: providers.ProviderAnthropic, Reason: "hmm_policy"}
	caches := sessionpin.ModelCaches{
		"claude-opus-4-8":   {ServedAt: now.Add(-20 * time.Second), PromptTokens: 400_000, MessageCount: 300, WriteTTLSeconds: 3600},
		"claude-sonnet-4-6": {ServedAt: now.Add(-10 * time.Minute), PromptTokens: 350_000, MessageCount: 260, WriteTTLSeconds: 300},
	}
	cases := []struct {
		name       string
		stay       sessionpin.Pin
		fresh      router.Decision
		served     continuity.CostGateServe
		wantOff    string
		wantOn     string
		wantSticky bool
		wantTier   string
	}{
		{
			name: "holds a downgrade the live gate takes when the rewrite exceeds ten turns of savings",
			stay: warmOpus, fresh: sonnet,
			served:  continuity.CostGateServe{Decision: sonnet, StayModel: warmOpus.Model, PinTier: "hmm_ev_switch_ev_positive"},
			wantOff: "claude-sonnet-4-6", wantOn: "claude-opus-4-8", wantSticky: true,
			wantTier: "decide_switch_" + string(SwitchReasonDowngradePending),
		},
		{
			name: "takes an upgrade the live gate holds when the current cache is cold",
			stay: coldHaiku, fresh: opus,
			served:  continuity.CostGateServe{Decision: coldHaiku.Decision(), Sticky: true, StayModel: coldHaiku.Model, PinTier: "hmm_ev_stay_ev_negative", UpgradeConfidence: 0.9},
			wantOff: "claude-haiku-5-5", wantOn: "claude-opus-5-5", wantSticky: false,
			wantTier: "decide_switch_" + string(SwitchReasonUpgrade),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn := SessionTurn{
				Pin: tc.stay, PinFound: true, Fresh: tc.fresh, PlannerTokens: 401_000,
				SwitchHistory: sessionpin.Pin{ModelCaches: caches},
				Ports:         continuity.StrategyPorts{CostGate: fixedCostGate{stay: tc.stay, served: tc.served}, History: roleOnlyHistory{}, Facts: pinStateOnlyFacts{}},
				Now:           func() time.Time { return now },
			}
			route := func(on bool) TurnRoute {
				res := TurnRoute{PinRole: "main", cacheShape: TurnCacheShape{MessageCount: 302, UserPrompt: true}}
				return turn.serveCostGate(switchAuthoritativeContext(on), &res)
			}

			off, on := route(false), route(true)

			assert.Equal(t, tc.wantOff, off.Decision.Model)
			assert.Equal(t, tc.served.PinTier, off.PinTier)
			assert.Equal(t, tc.wantOn, on.Decision.Model)
			assert.Equal(t, providers.ProviderAnthropic, on.Decision.Provider)
			assert.Equal(t, tc.wantSticky, on.StickyHit)
			assert.Equal(t, tc.wantTier, on.PinTier)
		})
	}
}

func TestServeCostGateKeepsTheLiveDecisionWhenDecideSwitchAgrees(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	haiku := sessionpin.Pin{Model: "claude-haiku-5-5", Provider: providers.ProviderAnthropic, LastServedModel: "claude-haiku-5-5", LastTurnEndedAt: now.Add(-20 * time.Second), Reason: "hmm_history"}
	held := haiku.Decision()
	held.Effort = "high"
	turn := SessionTurn{
		Pin: haiku, PinFound: true, Fresh: router.Decision{Model: "claude-opus-5-5", Provider: providers.ProviderAnthropic},
		Ports: continuity.StrategyPorts{CostGate: fixedCostGate{stay: haiku, served: continuity.CostGateServe{
			Decision: held, Sticky: true, StayModel: haiku.Model, PinTier: "hmm_ev_stay_upgrade_confidence_low", UpgradeConfidence: 0.2, UpgradeConfidenceLow: true,
		}}, History: roleOnlyHistory{}, Facts: pinStateOnlyFacts{}},
		Now: func() time.Time { return now },
	}
	res := TurnRoute{PinRole: "main", cacheShape: TurnCacheShape{MessageCount: 40, UserPrompt: true}}

	got := turn.serveCostGate(switchAuthoritativeContext(true), &res)

	assert.Equal(t, held, got.Decision)
	assert.Equal(t, "hmm_ev_stay_upgrade_confidence_low", got.PinTier)
	assert.True(t, got.StickyHit)
}

func TestFreshPickSwitchOutcomeAppliesDecideSwitchWithFullConfidenceOnlyWhenTheBenchmarkFlagIsOn(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	warmOpus := sessionpin.Pin{
		Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic, LastServedModel: "claude-opus-4-8", LastTurnEndedAt: now.Add(-20 * time.Second),
		LastInputTokens: 1_000, LastCachedReadTokens: 399_000,
	}
	coldHaiku := sessionpin.Pin{
		Model: "claude-haiku-5-5", Provider: providers.ProviderAnthropic, LastServedModel: "claude-haiku-5-5", LastTurnEndedAt: now.Add(-3 * time.Hour),
	}
	caches := sessionpin.ModelCaches{
		"claude-opus-4-8": {ServedAt: now.Add(-20 * time.Second), PromptTokens: 400_000, MessageCount: 300, WriteTTLSeconds: 3600},
	}
	cases := []struct {
		name      string
		pin       sessionpin.Pin
		fresh     router.Decision
		wantStay  bool
		wantTier  string
		wantApply bool
	}{
		{
			name: "holds a random downgrade whose rewrite exceeds ten turns of savings",
			pin:  warmOpus, fresh: router.Decision{Model: "claude-sonnet-4-6", Provider: providers.ProviderAnthropic},
			wantStay: true, wantTier: "decide_switch_" + string(SwitchReasonDowngradePending), wantApply: true,
		},
		{
			name: "takes a random upgrade off a cold cache without a confidence hold",
			pin:  coldHaiku, fresh: router.Decision{Model: "claude-opus-5-5", Provider: providers.ProviderAnthropic},
			wantStay: false, wantTier: "decide_switch_" + string(SwitchReasonUpgrade), wantApply: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn := SessionTurn{
				Pin: tc.pin, PinFound: true, Fresh: tc.fresh, PlannerTokens: 401_000,
				SwitchHistory: sessionpin.Pin{ModelCaches: caches},
				Now:           func() time.Time { return now },
			}
			res := TurnRoute{PinRole: "main", cacheShape: TurnCacheShape{MessageCount: 302, UserPrompt: true}}

			_, offApplied := turn.freshPickSwitchOutcome(switchAuthoritativeContext(false), res)
			got, applied := turn.freshPickSwitchOutcome(switchAuthoritativeContext(true), res)

			assert.False(t, offApplied)
			assert.Equal(t, tc.wantApply, applied)
			assert.Equal(t, tc.wantStay, got.Stay)
			assert.Equal(t, tc.wantTier, got.PinTier)
		})
	}
}
