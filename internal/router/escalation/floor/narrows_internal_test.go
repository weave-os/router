package floor

import (
	"maps"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
)

var (
	narrowingModels = []string{"model-a", "model-b", "model-c", "model-d", "model-e"}
	narrowingGroups = []escalation.Group{"", escalation.Low, escalation.Medium, escalation.High, escalation.Maximum}
)

type narrowingCase struct {
	req  router.Request
	turn *Turn
}

func (narrowingCase) Generate(r *rand.Rand, _ int) reflect.Value {
	modelSet := func() map[string]struct{} {
		if r.Intn(3) == 0 {
			return nil
		}
		set := map[string]struct{}{}
		for _, model := range narrowingModels {
			if r.Intn(2) == 0 {
				set[model] = struct{}{}
			}
		}
		return set
	}
	group := func() escalation.Group { return narrowingGroups[r.Intn(len(narrowingGroups))] }
	req := router.Request{Candidates: router.Candidates{
		ExcludedModels: modelSet(), AllowedModels: modelSet(), SafetyExcludedModels: modelSet(),
		ContextWindowExcludedModels: modelSet(), ImageLimitExcludedModels: modelSet(),
		UnsignedHistoryExcludedModels: modelSet(), OverflowAdmittedModels: modelSet(), AutomaticExcludedModels: modelSet(),
	},
		RequestedModel:       narrowingModels[r.Intn(len(narrowingModels))],
		ForceCluster:         string(group()),
		SkipSessionPin:       r.Intn(2) == 0,
		EstimatedInputTokens: r.Intn(200_000),
		HasTools:             r.Intn(2) == 0,
		PreferredModels:      []string{narrowingModels[r.Intn(len(narrowingModels))]},
		ClientSessionID:      "session",
	}
	if r.Intn(2) == 0 {
		req.Escalation = &escalation.Constraint{Floor: group(), Escalate: r.Intn(2) == 0}
	}
	var turn *Turn
	if r.Intn(5) != 0 {
		turn = &Turn{}
		if r.Intn(3) != 0 {
			turn.xgb = &xgbTurn{active: r.Intn(2) == 0, replay: r.Intn(4) == 0, session: escalation.Session{Floor: group()},
				checkpoint: escalation.Checkpoint{Prediction: &escalation.Prediction{Escalate: r.Intn(2) == 0}}}
		}
		if r.Intn(3) != 0 {
			turn.llm = &llmTurn{active: r.Intn(2) == 0, session: llmescalation.Session{Floor: group(),
				Pending: &llmescalation.Job{ID: "job", Judgment: &llmescalation.Judgment{Escalate: r.Intn(2) == 0}}}}
		}
	}
	return reflect.ValueOf(narrowingCase{req: req, turn: turn})
}

func offeredModels(c router.Candidates) map[string]bool {
	offered := map[string]bool{}
	for _, model := range narrowingModels {
		if _, violates := c.HardFilterViolation(model); !violates && !c.AutomaticallyExcludes(model) {
			offered[model] = true
		}
	}
	return offered
}

func cloneCandidates(c router.Candidates) router.Candidates {
	return router.Candidates{
		ExcludedModels: maps.Clone(c.ExcludedModels), AllowedModels: maps.Clone(c.AllowedModels), SafetyExcludedModels: maps.Clone(c.SafetyExcludedModels),
		ContextWindowExcludedModels: maps.Clone(c.ContextWindowExcludedModels), ImageLimitExcludedModels: maps.Clone(c.ImageLimitExcludedModels),
		UnsignedHistoryExcludedModels: maps.Clone(c.UnsignedHistoryExcludedModels), OverflowAdmittedModels: maps.Clone(c.OverflowAdmittedModels),
		AutomaticExcludedModels: maps.Clone(c.AutomaticExcludedModels),
	}
}

// escalationNarrowsOnly holds when applying the turn offers no model the input
// did not offer and changes nothing on the request but its escalation constraint.
func escalationNarrowsOnly(apply func(*Turn, *router.Request)) func(narrowingCase) bool {
	return func(tc narrowingCase) bool {
		input := tc.req
		input.Candidates = cloneCandidates(tc.req.Candidates)
		output := tc.req
		apply(tc.turn, &output)
		before := offeredModels(input.Candidates)
		for model := range offeredModels(output.Candidates) {
			if !before[model] {
				return false
			}
		}
		output.Escalation = input.Escalation
		return reflect.DeepEqual(input, output)
	}
}

func TestEscalationApplyOffersOnlyInputCandidates(t *testing.T) {
	require.NoError(t, quick.Check(escalationNarrowsOnly((*Turn).Apply), &quick.Config{MaxCount: 2000}))
}

func TestEscalationNarrowsOnlyRejectsAWidenedCandidateSet(t *testing.T) {
	widen := func(turn *Turn, req *router.Request) {
		turn.Apply(req)
		req.Candidates.ExcludedModels = nil
		req.Candidates.AllowedModels = nil
	}
	require.Error(t, quick.Check(escalationNarrowsOnly(widen), &quick.Config{MaxCount: 2000}))
	forceModel := func(turn *Turn, req *router.Request) {
		turn.Apply(req)
		req.ForceModel = "model-a"
	}
	require.Error(t, quick.Check(escalationNarrowsOnly(forceModel), &quick.Config{MaxCount: 2000}))
}
