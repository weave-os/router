package overrides_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy/overrides"
)

func TestHardPinsPinsTurn(t *testing.T) {
	explore := func(on bool) func() bool { return func() bool { return on } }
	subAgent := overrides.HardPins{SubAgentProvider: "anthropic", SubAgentModel: "claude-haiku-4-5"}
	tests := map[string]struct {
		pins     overrides.HardPins
		turnType turntype.TurnType
		explore  bool
		want     bool
	}{
		"probe always":                  {turnType: turntype.Probe, want: true},
		"title generation needs opt-in": {turnType: turntype.TitleGen},
		"title generation with opt-in":  {pins: overrides.HardPins{ExplicitUtility: true}, turnType: turntype.TitleGen, want: true},
		"sub-agent without override":    {turnType: turntype.SubAgentDispatch},
		"sub-agent explore":             {turnType: turntype.SubAgentDispatch, explore: true, want: true},
		"sub-agent override wins":       {pins: subAgent, turnType: turntype.SubAgentDispatch, want: true},
		"main loop never":               {pins: overrides.HardPins{ExplicitUtility: true}, turnType: turntype.MainLoop, explore: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.pins.PinsTurn(tt.turnType, explore(tt.explore)))
		})
	}
}

func TestHardPinsTarget(t *testing.T) {
	pins := overrides.HardPins{Provider: "openai", Model: "gpt-5-mini", Resolver: func(overrides.HardPinRequest) (string, string, bool) { return "", "", false }}
	resolver, provider, model := pins.Target(turntype.SubAgentDispatch)
	assert.NotNil(t, resolver)
	assert.Equal(t, []string{"openai", "gpt-5-mini"}, []string{provider, model})

	pins.SubAgentProvider, pins.SubAgentModel = "anthropic", "claude-haiku-4-5"
	resolver, provider, model = pins.Target(turntype.SubAgentDispatch)
	assert.Nil(t, resolver)
	assert.Equal(t, []string{"anthropic", "claude-haiku-4-5"}, []string{provider, model})

	_, provider, _ = pins.Target(turntype.Probe)
	assert.Equal(t, "openai", provider)
}
