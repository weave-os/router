package policy

import (
	"maps"
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
)

var (
	constrainGroups = []escalation.Group{"", escalation.Low, escalation.Medium, escalation.High, escalation.Maximum}
	constrainArms   = []string{"arm-a", "arm-b", "arm-c", "arm-d"}
)

type constrainCase struct {
	req   router.Request
	input SelectionInput
}

func (constrainCase) Generate(r *rand.Rand, _ int) reflect.Value {
	group := func() escalation.Group { return constrainGroups[r.Intn(len(constrainGroups))] }
	probabilities := map[string]float64{}
	for _, g := range constrainGroups[1:] {
		if r.Intn(2) == 0 {
			probabilities[string(g)] = r.Float64()
		}
	}
	var arms []string
	for _, arm := range constrainArms {
		if r.Intn(2) == 0 {
			arms = append(arms, arm)
		}
	}
	req := router.Request{RequestedModel: "requested"}
	if r.Intn(4) == 0 {
		req.ForceCluster = string(group())
	}
	if r.Intn(8) == 0 {
		req.ForceModel = constrainArms[r.Intn(len(constrainArms))]
	}
	if r.Intn(4) != 0 {
		req.Escalation = &escalation.Constraint{Floor: group(), Escalate: r.Intn(2) == 0}
	}
	input := SelectionInput{
		Strategy: "strategy", RouteID: "route", PredictedLabel: string(group()), ClassOrder: []string{"low", "high"},
		ClassProbabilities: probabilities, ForcedGroup: req.ForceCluster, LeadGroup: string(group()),
		CandidateRosterIDs: arms, PreferredModels: []string{"preferred"}, MinimumGroup: group(),
	}
	return reflect.ValueOf(constrainCase{req: req, input: input})
}

func offeredGroups(input SelectionInput) map[string]bool {
	if input.ForcedGroup != "" {
		return map[string]bool{input.ForcedGroup: true}
	}
	offered := map[string]bool{}
	for group := range input.ClassProbabilities {
		offered[group] = true
	}
	return offered
}

func isSubset(sub, super map[string]bool) bool {
	for key := range sub {
		if !super[key] {
			return false
		}
	}
	return true
}

func cloneSelectionInput(input SelectionInput) SelectionInput {
	input.ClassOrder = slices.Clone(input.ClassOrder)
	input.ClassProbabilities = maps.Clone(input.ClassProbabilities)
	input.CandidateRosterIDs = slices.Clone(input.CandidateRosterIDs)
	input.PreferredModels = slices.Clone(input.PreferredModels)
	return input
}

// constrainingNarrowsOnly holds when the constrained input offers no group or
// arm the input did not offer and changes nothing but the forced group.
func constrainingNarrowsOnly(constrain func(router.Request, SelectionInput) SelectionInput) func(constrainCase) bool {
	return func(tc constrainCase) bool {
		input := cloneSelectionInput(tc.input)
		output := constrain(tc.req, tc.input)
		armsBefore := map[string]bool{}
		for _, arm := range input.CandidateRosterIDs {
			armsBefore[arm] = true
		}
		armsAfter := map[string]bool{}
		for _, arm := range output.CandidateRosterIDs {
			armsAfter[arm] = true
		}
		if !isSubset(offeredGroups(output), offeredGroups(input)) || !isSubset(armsAfter, armsBefore) {
			return false
		}
		output.ForcedGroup = input.ForcedGroup
		return reflect.DeepEqual(input, output)
	}
}

func constrainEscalationInput(req router.Request, input SelectionInput) SelectionInput {
	constrained, _, _ := constrainEscalation(req, input, ResolvedCandidates{})
	return constrained
}

func TestConstrainEscalationOffersOnlyInputGroupsAndArms(t *testing.T) {
	require.NoError(t, quick.Check(constrainingNarrowsOnly(constrainEscalationInput), &quick.Config{MaxCount: 2000}))
}

func TestConstrainingNarrowsOnlyRejectsAnAddedArm(t *testing.T) {
	addArm := func(req router.Request, input SelectionInput) SelectionInput {
		constrained := constrainEscalationInput(req, input)
		constrained.CandidateRosterIDs = append(slices.Clone(constrained.CandidateRosterIDs), "arm-added")
		return constrained
	}
	require.Error(t, quick.Check(constrainingNarrowsOnly(addArm), &quick.Config{MaxCount: 2000}))
}
