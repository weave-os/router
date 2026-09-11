package policy_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
)

const (
	escalationSonnet    = "claude-sonnet-5"
	escalationOpus      = "claude-opus-4-8"
	escalationSonnetArm = "anthropic/" + escalationSonnet
	escalationOpusArm   = "anthropic/" + escalationOpus
)

func TestEscalationUsesNormalSelectorWithoutLosingFloor(t *testing.T) {
	tests := []struct {
		name            string
		baseline        escalation.Group
		floor           escalation.Group
		positive        bool
		emptyReported   []escalation.Group
		emptyRuntime    []escalation.Group
		overrides       map[string][]string
		lowerFirst      bool
		wantGroup       escalation.Group
		wantModel       string
		wantOutcome     escalation.Outcome
		wantConstrained bool
	}{
		{name: "low to medium", baseline: escalation.Low, positive: true, wantGroup: escalation.Medium, wantModel: escalationOpus, wantOutcome: escalation.OutcomePromoted, wantConstrained: true},
		{name: "medium to high", baseline: escalation.Medium, positive: true, wantGroup: escalation.High, wantModel: escalationSonnet, wantOutcome: escalation.OutcomePromoted, wantConstrained: true},
		{name: "high to maximum", baseline: escalation.High, positive: true, wantGroup: escalation.Maximum, wantModel: escalationOpus, wantOutcome: escalation.OutcomePromoted, wantConstrained: true},
		{name: "negative preserves prior floor", baseline: escalation.Low, floor: escalation.Medium, wantGroup: escalation.Medium, wantModel: escalationOpus, wantOutcome: escalation.OutcomeFloor, wantConstrained: true},
		{name: "equal baseline floor restricts fallback", baseline: escalation.Medium, floor: escalation.Medium, lowerFirst: true, wantGroup: escalation.Medium, wantModel: escalationOpus, wantOutcome: escalation.OutcomeFloor, wantConstrained: true},
		{name: "maximum positive does nothing", baseline: escalation.Maximum, positive: true, wantGroup: escalation.Maximum, wantModel: escalationOpus, wantOutcome: escalation.OutcomeMaximum},
		{name: "maximum positive keeps persisted floor", baseline: escalation.Maximum, floor: escalation.High, positive: true, wantGroup: escalation.Maximum, wantModel: escalationOpus, wantOutcome: escalation.OutcomeMaximum, wantConstrained: true},
		{name: "empty target does not skip a class", baseline: escalation.Low, positive: true, emptyReported: []escalation.Group{escalation.Medium}, wantGroup: escalation.Low, wantModel: escalationSonnet, wantOutcome: escalation.OutcomeNoTarget},
		{name: "empty target retains prior floor", baseline: escalation.Low, floor: escalation.Medium, positive: true, emptyReported: []escalation.Group{escalation.High}, wantGroup: escalation.Medium, wantModel: escalationOpus, wantOutcome: escalation.OutcomeNoTarget, wantConstrained: true},
		{name: "selector exhausted target retains prior floor", baseline: escalation.Low, floor: escalation.Medium, positive: true, emptyRuntime: []escalation.Group{escalation.High}, wantGroup: escalation.Medium, wantModel: escalationOpus, wantOutcome: escalation.OutcomeNoTarget, wantConstrained: true},
		{name: "equal baseline floor survives target exhaustion", baseline: escalation.Medium, floor: escalation.Medium, positive: true, lowerFirst: true, emptyRuntime: []escalation.Group{escalation.High}, wantGroup: escalation.Medium, wantModel: escalationOpus, wantOutcome: escalation.OutcomeNoTarget, wantConstrained: true},
		{name: "target and floor exhausted retain baseline", baseline: escalation.Low, floor: escalation.Medium, positive: true, emptyRuntime: []escalation.Group{escalation.High, escalation.Medium}, wantGroup: escalation.Low, wantModel: escalationSonnet, wantOutcome: escalation.OutcomeNoTarget},
		{name: "unrelated key override cannot lower floor", baseline: escalation.Low, floor: escalation.Medium, overrides: map[string][]string{string(escalation.Low): {escalationSonnet}}, wantGroup: escalation.Medium, wantModel: escalationOpus, wantOutcome: escalation.OutcomeFloor, wantConstrained: true},
		{name: "target override uses catalog IDs", baseline: escalation.Low, positive: true, overrides: map[string][]string{string(escalation.Medium): {escalationOpus}}, wantGroup: escalation.Medium, wantModel: escalationOpus, wantOutcome: escalation.OutcomePromoted, wantConstrained: true},
		{name: "target override may add an eligible arm", baseline: escalation.Low, positive: true, overrides: map[string][]string{string(escalation.Medium): {escalationSonnet}}, wantGroup: escalation.Medium, wantModel: escalationSonnet, wantOutcome: escalation.OutcomePromoted, wantConstrained: true},
		{name: "empty target override prevents promotion", baseline: escalation.Low, positive: true, overrides: map[string][]string{string(escalation.Medium): {}}, wantGroup: escalation.Low, wantModel: escalationSonnet, wantOutcome: escalation.OutcomeNoTarget},
		{name: "negative without floor leaves selection unchanged", baseline: escalation.Low, wantGroup: escalation.Low, wantModel: escalationSonnet, wantOutcome: escalation.OutcomeBelowThreshold},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			roster := &rosterdata.Roster{
				SchemaVersion: rosterdata.SchemaVersionV6,
				Clusters: map[string]rosterdata.Cluster{
					string(escalation.Low):     {Arms: []string{escalationSonnetArm}},
					string(escalation.Medium):  {Arms: []string{escalationOpusArm}},
					string(escalation.High):    {Arms: []string{escalationSonnetArm}},
					string(escalation.Maximum): {Arms: []string{escalationOpusArm}},
				},
			}
			groups := []escalation.Group{test.baseline}
			for _, group := range []escalation.Group{escalation.Low, escalation.Medium, escalation.High, escalation.Maximum} {
				if group != test.baseline {
					groups = append(groups, group)
				}
			}
			if test.lowerFirst {
				groups = []escalation.Group{escalation.Low, escalation.Medium, escalation.High, escalation.Maximum}
			}
			classification := policy.Result{
				SchemaVersion: policy.SchemaVersionV4, PredictedLabel: string(test.baseline),
				ClassProbabilities: make(map[string]float64, len(groups)),
			}
			weightTotal := float64(len(groups)*(len(groups)+1)) / 2
			for index, group := range groups {
				classification.ClassOrder = append(classification.ClassOrder, string(group))
				classification.ClassProbabilities[string(group)] = float64(len(groups)-index) / weightTotal
			}
			for _, group := range append(test.emptyReported, test.emptyRuntime...) {
				delete(roster.Clusters, string(group))
			}
			adapter := newSelectorAdapter(classification).WithArmSelector(selection.Selector(roster))
			decision, err := adapter.Route(context.Background(), router.Request{
				Escalation:          &escalation.Constraint{Floor: test.floor, Escalate: test.positive},
				ClusterArmOverrides: test.overrides,
			})
			require.NoError(t, err)
			require.NotNil(t, decision.Metadata)
			require.NotNil(t, decision.Metadata.Escalation)
			assert.Equal(t, test.wantModel, decision.Model)
			assert.Equal(t, string(test.wantGroup), decision.Metadata.PolicyGroup)
			assert.Equal(t, test.baseline, decision.Metadata.Escalation.Baseline)
			assert.Equal(t, test.wantGroup, decision.Metadata.Escalation.Effective)
			assert.Equal(t, test.wantOutcome, decision.Metadata.Escalation.Outcome)
			assert.Equal(t, test.wantConstrained, decision.Metadata.Escalation.Constrained)
		})
	}
}

func TestEscalationYieldsToExplicitClusterForce(t *testing.T) {
	classification := classifierOnlyResult()
	roster := &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionV6,
		Clusters: map[string]rosterdata.Cluster{
			string(escalation.Low):     {Arms: []string{escalationSonnetArm}},
			string(escalation.Maximum): {Arms: []string{escalationOpusArm}},
		},
	}
	adapter := newSelectorAdapter(classification).WithArmSelector(selection.Selector(roster))
	decision, err := adapter.Route(context.Background(), router.Request{
		ForceCluster: string(escalation.Low),
		Escalation:   &escalation.Constraint{Floor: escalation.High, Escalate: true},
	})
	require.NoError(t, err)
	assert.Equal(t, escalationSonnet, decision.Model)
	assert.Equal(t, string(escalation.Low), decision.Metadata.PolicyGroup)
	assert.Nil(t, decision.Metadata.Escalation)
}
