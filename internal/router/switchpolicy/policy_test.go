package switchpolicy_test

import (
	"errors"
	"math"
	"strconv"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/switchpolicy"
)

const (
	modelSol   = "gpt-6.1-sol"
	modelTerra = "gpt-6.1-terra"
)

var (
	sol    = switchpolicy.Selection{Target: switchpolicy.Target{Provider: providers.ProviderOpenAI, Model: modelSol}, Group: escalation.High}
	terra  = switchpolicy.Selection{Target: switchpolicy.Target{Provider: providers.ProviderOpenAI, Model: modelTerra}, Group: escalation.Medium}
	luna   = switchpolicy.Selection{Target: switchpolicy.Target{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}, Group: escalation.Low}
	config = switchpolicy.Config{MinimumConfidence: .5, UpgradeVotes: 3, DowngradeVotes: 5}
)

func observe(t *testing.T, state *switchpolicy.State, proposal switchpolicy.Selection, confidence float64) switchpolicy.Decision {
	t.Helper()
	observation := switchpolicy.Observation{
		ID: strconv.FormatUint(state.Revision+1, 10), ExpectedRevision: state.Revision,
		Proposal: proposal, Confidence: confidence, Eligible: true,
	}
	decision, err := switchpolicy.Evaluate(config, *state, observation)
	if err != nil {
		t.Fatal(err)
	}
	*state, err = switchpolicy.Commit(*state, decision)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func TestAgreementRequiresSameDestination(t *testing.T) {
	state := switchpolicy.State{Current: sol}
	for _, proposal := range []switchpolicy.Selection{terra, terra, luna, terra, luna} {
		decision := observe(t, &state, proposal, .9)
		if decision.Switched || state.Current != sol {
			t.Fatalf("mixed cheaper proposals switched incumbent: %+v", decision)
		}
	}
	if state.Votes != 1 || state.Challenger != luna {
		t.Fatalf("destination change did not restart the run: %+v", state)
	}
	for vote := 2; vote <= 5; vote++ {
		decision := observe(t, &state, luna, .9)
		if decision.Switched != (vote == 5) || decision.Votes != vote {
			t.Fatalf("vote %d: %+v", vote, decision)
		}
	}
	if state.Current != luna || state.Votes != 0 || state.Challenger != (switchpolicy.Selection{}) {
		t.Fatalf("accepted switch must clear challenger: %+v", state)
	}
}

func TestUpgradeDoesNotBypassAgreement(t *testing.T) {
	state := switchpolicy.State{Current: luna}
	for vote := 1; vote <= 3; vote++ {
		decision := observe(t, &state, sol, .99)
		if decision.Switched != (vote == 3) || decision.RequiredVotes != 3 || decision.Direction != switchpolicy.DirectionUpgrade {
			t.Fatalf("high confidence bypassed upgrade agreement on vote %d: %+v", vote, decision)
		}
	}
}

func TestDisqualifyingObservationResetsAgreement(t *testing.T) {
	tests := []struct {
		name       string
		proposal   switchpolicy.Selection
		confidence float64
		eligible   bool
		reason     switchpolicy.Reason
	}{
		{"incumbent confirmation", luna, .9, true, switchpolicy.ReasonIncumbent},
		{"confidence below threshold", sol, .49, true, switchpolicy.ReasonConfidence},
		{"ineligible proposal", sol, .9, false, switchpolicy.ReasonIneligible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := switchpolicy.State{Current: luna}
			observe(t, &state, sol, .9)
			observe(t, &state, sol, .9)
			decision, err := switchpolicy.Evaluate(config, state, switchpolicy.Observation{
				ID: "interrupt", ExpectedRevision: state.Revision, Proposal: test.proposal,
				Confidence: test.confidence, Eligible: test.eligible,
			})
			if err != nil {
				t.Fatal(err)
			}
			state, err = switchpolicy.Commit(state, decision)
			if err != nil || decision.Reason != test.reason || state.Votes != 0 {
				t.Fatalf("interrupt did not clear the run: %+v, %v", decision, err)
			}
			decision = observe(t, &state, sol, .9)
			if decision.Switched || decision.Votes != 1 {
				t.Fatalf("vote crossed an interrupted run: %+v", decision)
			}
		})
	}
}

func TestDestinationIncludesProviderAndQualityGroup(t *testing.T) {
	state := switchpolicy.State{Current: luna}
	observe(t, &state, sol, .9)
	observe(t, &state, sol, .9)
	otherProvider := sol
	otherProvider.Target.Provider = providers.ProviderOpenRouter
	decision := observe(t, &state, otherProvider, .9)
	if decision.Switched || decision.Votes != 1 {
		t.Fatalf("provider change reused another destination's votes: %+v", decision)
	}
	otherGroup := otherProvider
	otherGroup.Group = escalation.Maximum
	decision = observe(t, &state, otherGroup, .9)
	if decision.Switched || decision.Votes != 1 {
		t.Fatalf("group change reused a different qualification's votes: %+v", decision)
	}
}

func TestLateralMovesUseConservativeAgreement(t *testing.T) {
	state := switchpolicy.State{Current: terra}
	withinGroup := sol
	withinGroup.Group = escalation.Medium
	for vote := 1; vote <= 5; vote++ {
		decision := observe(t, &state, withinGroup, .9)
		if decision.Direction != switchpolicy.DirectionLateral || decision.RequiredVotes != 5 || decision.Switched != (vote == 5) {
			t.Fatalf("lateral vote %d: %+v", vote, decision)
		}
	}
}

func TestConfidenceBoundaryAndQualityDirection(t *testing.T) {
	state := switchpolicy.State{Current: terra}
	// The group, not this model's catalog input price, defines direction.
	lowerGroupSol := sol
	lowerGroupSol.Group = escalation.Low
	decision := observe(t, &state, lowerGroupSol, config.MinimumConfidence)
	if decision.Reason != switchpolicy.ReasonAgreement || decision.Votes != 1 || decision.Direction != switchpolicy.DirectionDowngrade {
		t.Fatalf("qualifying boundary or direction is inconsistent: %+v", decision)
	}
}

func TestSameBaseModelDoesNotBecomeModelSwitch(t *testing.T) {
	state := switchpolicy.State{Current: sol, Challenger: terra, Votes: 2}
	maximumSol := sol
	maximumSol.Group = escalation.Maximum
	decision := observe(t, &state, maximumSol, .9)
	if decision.Switched || decision.Direction != switchpolicy.DirectionUnchanged || state.Current != sol || state.Votes != 0 {
		t.Fatalf("effort/group metadata was counted as a model switch: %+v", decision)
	}
}

func TestIncumbentLabelDoesNotReclassifyNextSwitch(t *testing.T) {
	state := switchpolicy.State{Current: sol}
	lowSol := sol
	lowSol.Group = escalation.Low
	observe(t, &state, lowSol, .9)
	decision := observe(t, &state, terra, .9)
	if decision.Direction != switchpolicy.DirectionDowngrade || decision.RequiredVotes != 5 {
		t.Fatalf("one same-model label weakened downgrade agreement: %+v", decision)
	}
}

func TestConfigurationChangeDoesNotBorrowOldVotes(t *testing.T) {
	state := switchpolicy.State{Current: luna}
	observe(t, &state, sol, .9)
	observe(t, &state, sol, .9)
	changed := config
	changed.MinimumConfidence = .8
	decision, err := switchpolicy.Evaluate(changed, state, switchpolicy.Observation{
		ID: "new-policy", ExpectedRevision: state.Revision, Proposal: sol, Confidence: .9, Eligible: true,
	})
	if err != nil || decision.Switched || decision.Votes != 1 {
		t.Fatalf("configuration change reused an old agreement run: %+v, %v", decision, err)
	}
}

func TestInitialSelectionAndExplicitOverrides(t *testing.T) {
	state := switchpolicy.State{}
	decision := observe(t, &state, luna, .25)
	if decision.Switched || decision.Reason != switchpolicy.ReasonInitial || state.Current != luna {
		t.Fatalf("first selection requires an incumbent to hold: %+v", decision)
	}
	for _, override := range []switchpolicy.Override{
		switchpolicy.OverrideManual, switchpolicy.OverrideIncumbentIneligible,
		switchpolicy.OverrideRecovery, switchpolicy.OverrideQualityFloor,
	} {
		t.Run(string(override), func(t *testing.T) {
			current := switchpolicy.State{Current: luna}
			decision, err := switchpolicy.Evaluate(config, current, switchpolicy.Observation{
				ID: "override", Proposal: sol, Eligible: true, Override: override,
			})
			if err != nil || !decision.Switched || decision.Override != override || decision.Reason != switchpolicy.ReasonOverride {
				t.Fatalf("mandatory exception was held: %+v, %v", decision, err)
			}
		})
	}
	_, err := switchpolicy.Evaluate(config, switchpolicy.State{Current: sol}, switchpolicy.Observation{
		ID: "bad-floor", Proposal: luna, Eligible: true, Override: switchpolicy.OverrideQualityFloor,
	})
	if err == nil {
		t.Fatal("quality-floor override must not downgrade an already higher incumbent")
	}
}

func TestRetryAndStaleCompetingCommit(t *testing.T) {
	state := switchpolicy.State{Current: luna}
	observation := switchpolicy.Observation{ID: "a", Proposal: sol, Confidence: .9, Eligible: true}
	first, err := switchpolicy.Evaluate(config, state, observation)
	if err != nil {
		t.Fatal(err)
	}
	competingObservation := observation
	competingObservation.ID = "b"
	competing, err := switchpolicy.Evaluate(config, state, competingObservation)
	if err != nil {
		t.Fatal(err)
	}
	state, err = switchpolicy.Commit(state, first)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged, err := switchpolicy.Commit(state, competing); !errors.Is(err, switchpolicy.ErrStaleRevision) || unchanged != state {
		t.Fatalf("stale commit overwrote newer votes: %+v, %v", unchanged, err)
	}
	if _, err := switchpolicy.Evaluate(config, state, competingObservation); !errors.Is(err, switchpolicy.ErrStaleRevision) {
		t.Fatalf("stale read must be reloaded, got %v", err)
	}
	retry, err := switchpolicy.Evaluate(config, state, observation)
	if err != nil || retry.Reason != switchpolicy.ReasonDuplicate || retry.Next != state {
		t.Fatalf("retry counted the same observation twice: %+v, %v", retry, err)
	}
	competingObservation.ExpectedRevision = state.Revision
	updated, err := switchpolicy.Evaluate(config, state, competingObservation)
	if err != nil || updated.Votes != 2 {
		t.Fatalf("re-evaluated observation lost the first vote: %+v, %v", updated, err)
	}
	state, err = switchpolicy.Commit(state, updated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := switchpolicy.Evaluate(config, state, observation); !errors.Is(err, switchpolicy.ErrStaleRevision) {
		t.Fatalf("older retried observation must not join a newer run: %v", err)
	}
}

func TestInvalidBoundaryInputsFailClosed(t *testing.T) {
	valid := switchpolicy.Observation{ID: "valid", Proposal: luna, Confidence: .5, Eligible: true}
	tests := []struct {
		name   string
		mutate func(*switchpolicy.Config, *switchpolicy.State, *switchpolicy.Observation)
	}{
		{"zero votes", func(c *switchpolicy.Config, _ *switchpolicy.State, _ *switchpolicy.Observation) { c.UpgradeVotes = 0 }},
		{"negative votes", func(c *switchpolicy.Config, _ *switchpolicy.State, _ *switchpolicy.Observation) {
			c.DowngradeVotes = -1
		}},
		{"NaN threshold", func(c *switchpolicy.Config, _ *switchpolicy.State, _ *switchpolicy.Observation) {
			c.MinimumConfidence = math.NaN()
		}},
		{"infinite probability", func(_ *switchpolicy.Config, _ *switchpolicy.State, o *switchpolicy.Observation) {
			o.Confidence = math.Inf(1)
		}},
		{"negative probability", func(_ *switchpolicy.Config, _ *switchpolicy.State, o *switchpolicy.Observation) { o.Confidence = -.1 }},
		{"probability above one", func(_ *switchpolicy.Config, _ *switchpolicy.State, o *switchpolicy.Observation) { o.Confidence = 1.1 }},
		{"invalid group", func(_ *switchpolicy.Config, _ *switchpolicy.State, o *switchpolicy.Observation) {
			o.Proposal.Group = "unknown"
		}},
		{"unknown override", func(_ *switchpolicy.Config, _ *switchpolicy.State, o *switchpolicy.Observation) {
			o.Override = "cold_cache"
		}},
		{"missing id", func(_ *switchpolicy.Config, _ *switchpolicy.State, o *switchpolicy.Observation) { o.ID = "" }},
		{"missing provider", func(_ *switchpolicy.Config, _ *switchpolicy.State, o *switchpolicy.Observation) {
			o.Proposal.Target.Provider = ""
		}},
		{"ineligible first target", func(_ *switchpolicy.Config, _ *switchpolicy.State, o *switchpolicy.Observation) { o.Eligible = false }},
		{"negative state votes", func(_ *switchpolicy.Config, s *switchpolicy.State, _ *switchpolicy.Observation) { s.Votes = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, state, observation := config, switchpolicy.State{}, valid
			test.mutate(&c, &state, &observation)
			if _, err := switchpolicy.Evaluate(c, state, observation); err == nil {
				t.Fatal("invalid boundary value was accepted")
			}
		})
	}
}
