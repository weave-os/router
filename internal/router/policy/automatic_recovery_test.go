package policy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
)

func TestAutomaticExclusionsRecoverWithinServingRoster(t *testing.T) {
	model := catalog.ModelIDClaudeOpus48.String()
	arm := providers.ProviderAnthropic + "/" + model
	group := string(escalation.High)
	for _, unscorable := range []bool{false, true} {
		for _, hardExcluded := range []bool{false, true} {
			name := "scored"
			if unscorable {
				name = "unscorable"
			}
			if hardExcluded {
				name += "/hard-exclusion"
			}
			t.Run(name, func(t *testing.T) {
				roster := &rosterdata.Roster{
					ClassOrder: []string{group},
					Clusters:   map[string]rosterdata.Cluster{group: {Arms: []string{arm}}},
				}
				decider := &recordingPolicy{result: policy.Result{
					SchemaVersion: policy.SchemaVersionV4, ClassOrder: roster.ClassOrder,
					PredictedLabel: group, ClassProbabilities: map[string]float64{group: 1},
				}}
				// The off-roster model keeps the catalog pool nonempty after the
				// last serving model is struck by subscription recovery.
				resolver := policy.NewResolver(set(model, catalog.ModelIDClaudeHaiku45.String()), set(providers.ProviderAnthropic),
					func(m catalog.Model) string { return providers.ProviderAnthropic + "/" + m.ID }, policy.ManagedProviderPolicy())
				adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMMEmbedding}, decider, resolver)
				adapter.WithArmSelector(selection.Selector(roster))
				request := router.Request{Unscorable: unscorable, AutomaticExcludedModels: set(model)}
				if hardExcluded {
					request.ExcludedModels = set(model)
				}
				decision, err := adapter.Route(context.Background(), request)
				if hardExcluded {
					require.ErrorIs(t, err, policy.ErrNoEligibleArm)
					return
				}
				require.NoError(t, err)
				require.Equal(t, model, decision.Model)
				require.Equal(t, group, decision.Metadata.PolicyGroup)
				require.Contains(t, decision.Metadata.CandidateModels, model)
				require.Contains(t, request.AutomaticExcludedModels, model, "recovery must not mutate caller state")
			})
		}
	}
}

func TestAutomaticReadmissionPreservesRequestConstraints(t *testing.T) {
	model := catalog.ModelIDClaudeOpus48.String()
	other := catalog.ModelIDClaudeHaiku45.String()
	group := string(escalation.High)
	arm := providers.ProviderAnthropic + "/" + model
	for _, scenario := range []struct {
		name    string
		request router.Request
	}{
		{name: "allowlist", request: router.Request{AllowedModels: set(other)}},
		{name: "context", request: router.Request{EstimatedInputTokens: catalog.ContextWindowFor(model) + 1}},
		{name: "unsigned history", request: router.Request{UnsignedHistoryExcludedModels: set(model)}},
		{name: "image limit", request: router.Request{ImageLimitExcludedModels: set(model)}},
		{name: "provider", request: router.Request{EnabledProviders: set(providers.ProviderOpenAI)}},
		{name: "forced group", request: router.Request{ForceCluster: string(escalation.Low)}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			adapter := newSelectorAdapter(policy.Result{
				SchemaVersion: policy.SchemaVersionV4, ClassOrder: []string{group}, PredictedLabel: group,
				ClassProbabilities: map[string]float64{group: 1},
			})
			adapter.WithArmSelector(selection.Selector(&rosterdata.Roster{
				ClassOrder: []string{group}, Clusters: map[string]rosterdata.Cluster{group: {Arms: []string{arm}}},
			}))
			request := scenario.request
			request.AutomaticExcludedModels = set(model)
			request.AutomaticExcludedModels[other] = struct{}{}
			_, err := adapter.Route(context.Background(), request)
			require.Error(t, err)
		})
	}
}

func TestAutomaticReadmissionDoesNotRetryClassifierFailure(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	calls := 0
	contractErr := errors.New("classifier identity rejected")
	adapter.WithArmSelector(func(context.Context, policy.SelectionInput) (policy.SelectionPick, error) {
		calls++
		return policy.SelectionPick{}, contractErr
	})
	_, err := adapter.Route(context.Background(), router.Request{AutomaticExcludedModels: set(catalog.ModelIDClaudeOpus48.String())})
	require.ErrorIs(t, err, contractErr)
	require.Equal(t, 1, calls)
}

func TestAutomaticReadmissionPreviewUsesSameRoster(t *testing.T) {
	model := catalog.ModelIDClaudeOpus48.String()
	arm := providers.ProviderAnthropic + "/" + model
	group := string(escalation.High)
	decider := &recordingPolicy{preview: policy.PreviewResult{
		SchemaVersion: policy.SchemaVersionV4, ClassOrder: []string{group},
		ClassProbabilities: map[string]float64{group: 1}, PredictedLabel: group,
		PolicyArtifactID: "synthetic-classifier", PolicyArtifactSHA256: "synthetic-sha",
	}}
	resolver := policy.NewResolver(set(model, catalog.ModelIDClaudeHaiku45.String()), set(providers.ProviderAnthropic),
		func(m catalog.Model) string { return providers.ProviderAnthropic + "/" + m.ID }, policy.ManagedProviderPolicy())
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMMEmbedding}, decider, resolver)
	adapter.WithArmSelector(selection.Selector(&rosterdata.Roster{
		ClassOrder: []string{group}, Clusters: map[string]rosterdata.Cluster{group: {Arms: []string{arm}}},
	}))
	preview, err := adapter.PreviewRoute(context.Background(), router.Request{AutomaticExcludedModels: set(model)})
	require.NoError(t, err)
	require.Equal(t, []string{arm}, preview.EligibleRosterIDs)
	require.Equal(t, group, preview.SelectedGroup)
	require.Len(t, preview.ResolverCandidates, 2)
}
