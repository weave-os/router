package selection_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/hmm/selectionpolicy"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/taskdomain"

	"github.com/stretchr/testify/require"
)

var updateSelectionGoldens = flag.Bool("update", false, "rewrite selection golden files from current output")

const (
	syntheticPolicyPath           = "testdata/golden/policy_v1_synthetic.json"
	syntheticDomainEvidencePath   = "testdata/golden/domain_evidence_synthetic.json"
	syntheticPolicySHA256         = "2a384cad8c6b8e06c785b99e9bbc39079d177e78419abdd0a356658101415a31"
	syntheticDomainEvidenceSHA256 = "ddaa7ed812fa8d7fe05279dadd407887291646dd412c7c0d0235c44a53ccb0fa"
	syntheticClassifierSHA256     = "5555555555555555555555555555555555555555555555555555555555555555"
	syntheticTaskReleaseSHA256    = "6666666666666666666666666666666666666666666666666666666666666666"
	unloadedRosterSHA256          = "7777777777777777777777777777777777777777777777777777777777777777"
	unboundEvidenceSHA256         = "9999999999999999999999999999999999999999999999999999999999999999"
)

type syntheticClassifier struct {
	taskDomain *taskdomain.Outcome
}

func (c syntheticClassifier) Decide(context.Context, policy.Query) (policy.Result, error) {
	return policy.Result{
		SchemaVersion:        policy.SchemaVersionV4,
		PolicyArtifactID:     "synthetic-classifier",
		PolicyArtifactSHA256: syntheticClassifierSHA256,
		PredictedLabel:       "low",
		ClassOrder:           []string{"low", "medium", "high", "maximum"},
		ClassProbabilities:   map[string]float64{"low": 0.6, "medium": 0.25, "high": 0.1, "maximum": 0.05},
		TaskDomain:           c.taskDomain,
	}, nil
}

type selectionGolden struct {
	Error               string                 `json:"error,omitempty"`
	Model               string                 `json:"model,omitempty"`
	Provider            string                 `json:"provider,omitempty"`
	Effort              string                 `json:"effort,omitempty"`
	Reason              string                 `json:"reason,omitempty"`
	PolicyGroup         string                 `json:"policy_group,omitempty"`
	SelectedRosterArmID string                 `json:"selected_roster_arm_id,omitempty"`
	PolicyPinHonoured   bool                   `json:"policy_pin_honoured,omitempty"`
	RescueModels        []string               `json:"rescue_models,omitempty"`
	Escalation          *escalation.Decision   `json:"escalation,omitempty"`
	SelectionTrace      *router.SelectionTrace `json:"selection_trace,omitempty"`
}

type selectionScenario struct {
	name       string
	request    router.Request
	pin        *router.PolicyPin
	taskDomain *taskdomain.Outcome
}

func float64Pointer(value float64) *float64 { return &value }

func readyInfraTaskDomain(evidenceSHA256 string) *taskdomain.Outcome {
	return &taskdomain.Outcome{
		Status:         taskdomain.Ready,
		ReleaseSHA256:  syntheticTaskReleaseSHA256,
		EvidenceSHA256: evidenceSHA256,
		Profile:        taskdomain.Profile{taskdomain.UI: false, taskdomain.Logic: false, taskdomain.Data: false, taskdomain.Infra: true, taskdomain.Docs: false},
	}
}

func selectionScenarios() []selectionScenario {
	return []selectionScenario{
		{name: "harness_order", request: router.Request{ClientApp: "claude-code"}},
		{name: "default_order_fallback", request: router.Request{ClientApp: "cursor"}},
		{name: "neutral_bias_pins_inert", request: router.Request{ClientApp: "claude-code", RoutingKnobs: &router.Overrides{QualityBias: float64Pointer(0.7)}}},
		{name: "off_neutral_pins_and_preferred_vendors", request: router.Request{ClientApp: "claude-code", RoutingKnobs: &router.Overrides{QualityBias: float64Pointer(0.9)}}},
		{name: "off_neutral_pins_default_order", request: router.Request{ClientApp: "cursor", RoutingKnobs: &router.Overrides{QualityBias: float64Pointer(0.2)}}},
		{name: "preferred_models_at_neutral", request: router.Request{ClientApp: "cursor", PreferredModels: []string{"claude-haiku-4-5"}}},
		{name: "out_of_group_subscription_ladder_lead", request: router.Request{ClientApp: "claude-code", SubscriptionProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}},
		{name: "subscription_without_ladder", request: router.Request{ClientApp: "codex", SubscriptionProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}},
		{name: "force_cluster", request: router.Request{ClientApp: "codex", ForceCluster: "medium"}},
		{name: "force_cluster_with_override", request: router.Request{ClientApp: "codex", ForceCluster: "medium", ClusterArmOverrides: map[string][]string{"medium": {"claude-sonnet-4-6"}}}},
		{name: "cluster_override", request: router.Request{ClientApp: "cursor", ClusterArmOverrides: map[string][]string{"low": {"claude-haiku-4-5"}}}},
		{name: "escalation_promote", request: router.Request{ClientApp: "cursor", Escalation: &escalation.Constraint{Escalate: true}}},
		{name: "escalation_floor", request: router.Request{ClientApp: "cursor", Escalation: &escalation.Constraint{Floor: escalation.High}}},
		{name: "rescue_order_after_group_exhausted", request: router.Request{ClientApp: "cursor", ExcludedModels: map[string]struct{}{"gpt-5.5-mini": {}, "gemini-3.5-flash": {}, "claude-haiku-4-5": {}}}},
		{name: "task_domain_evidence", request: router.Request{ClientApp: "cursor"}, taskDomain: readyInfraTaskDomain(syntheticDomainEvidenceSHA256)},
		{name: "task_domain_evidence_digest_mismatch", request: router.Request{ClientApp: "cursor"}, taskDomain: readyInfraTaskDomain(unboundEvidenceSHA256)},
		{name: "policy_pin_loaded_roster", request: router.Request{ClientApp: "cursor"}, pin: &router.PolicyPin{ArtifactSHA256: syntheticClassifierSHA256, RosterSHA256: syntheticPolicySHA256}},
		{name: "policy_pin_roster_sha_mismatch", request: router.Request{ClientApp: "cursor"}, pin: &router.PolicyPin{ArtifactSHA256: syntheticClassifierSHA256, RosterSHA256: unloadedRosterSHA256}},
	}
}

func loadSyntheticPolicy(t *testing.T) (*selectionpolicy.Policy, *selection.DomainEvidence) {
	t.Helper()
	policyBytes, err := os.ReadFile(syntheticPolicyPath)
	require.NoError(t, err)
	admitted, err := selectionpolicy.Admit(policyBytes)
	require.NoError(t, err)
	require.Equal(t, syntheticPolicySHA256, admitted.SHA256())
	evidenceBytes, err := os.ReadFile(syntheticDomainEvidencePath)
	require.NoError(t, err)
	evidence, err := selection.ParseDomainEvidence(evidenceBytes, admitted.Roster())
	require.NoError(t, err)
	return admitted, evidence
}

func routeScenario(t *testing.T, admitted *selectionpolicy.Policy, evidence *selection.DomainEvidence, scenario selectionScenario) selectionGolden {
	t.Helper()
	availableProviders := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}, providers.ProviderGoogle: {}}
	routed := hmm.NewForStrategy(router.StrategyHMM, syntheticClassifier{taskDomain: scenario.taskDomain}, availableProviders)
	routed.WithArmSelector(selection.Selector(admitted, evidence, syntheticDomainEvidenceSHA256))
	ctx := context.Background()
	if scenario.pin != nil {
		ctx = router.WithPolicyPinRequest(ctx, router.PolicyPinRequest{Pin: *scenario.pin, Authorized: true})
	}
	if scenario.request.RoutingKnobs != nil {
		ctx = router.WithRoutingKnobs(ctx, scenario.request.RoutingKnobs)
	}
	request := scenario.request
	request.PromptText = "synthetic prompt"
	decision, err := routed.Route(ctx, request)
	if err != nil {
		return selectionGolden{Error: err.Error()}
	}
	golden := selectionGolden{Model: decision.Model, Provider: decision.Provider, Effort: decision.Effort, Reason: decision.Reason}
	if metadata := decision.Metadata; metadata != nil {
		golden.PolicyGroup = metadata.PolicyGroup
		golden.SelectedRosterArmID = metadata.SelectedRosterArmID
		golden.PolicyPinHonoured = metadata.PolicyPinHonoured
		golden.RescueModels = metadata.RescueModels
		golden.Escalation = metadata.Escalation
		golden.SelectionTrace = rosterScopedTrace(metadata.SelectionTrace, admitted.Roster())
	}
	return golden
}

func rosterScopedTrace(trace *router.SelectionTrace, roster *rosterdata.Roster) *router.SelectionTrace {
	if trace == nil {
		return nil
	}
	rosterArms := make(map[string]struct{})
	for _, arm := range roster.AllArms() {
		rosterArms[arm] = struct{}{}
	}
	rosterModels := make(map[string]struct{})
	for _, deployed := range hmm.DeployedModelsForRosterIDs(roster.AllArms()) {
		rosterModels[deployed.Model] = struct{}{}
	}
	scoped := *trace
	scoped.CandidateRosterIDs = []string{}
	for _, rosterID := range trace.CandidateRosterIDs {
		if _, ok := rosterArms[rosterID]; ok {
			scoped.CandidateRosterIDs = append(scoped.CandidateRosterIDs, rosterID)
		}
	}
	scoped.ResolverExclusions = nil
	for _, exclusion := range trace.ResolverExclusions {
		_, rosterArm := rosterArms[exclusion.RosterID]
		_, rosterModel := rosterModels[exclusion.CatalogID]
		if rosterArm || rosterModel {
			scoped.ResolverExclusions = append(scoped.ResolverExclusions, exclusion)
		}
	}
	return &scoped
}

func TestSelectionGoldens(t *testing.T) {
	admitted, evidence := loadSyntheticPolicy(t)
	for _, scenario := range selectionScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			got, err := json.MarshalIndent(routeScenario(t, admitted, evidence, scenario), "", "  ")
			require.NoError(t, err)
			got = append(got, '\n')
			path := filepath.Join("testdata", "golden", "selection", scenario.name+".json")
			if *updateSelectionGoldens {
				require.NoError(t, os.WriteFile(path, got, 0o600))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "run go test ./internal/router/hmm/selection -run TestSelectionGoldens -update to create it")
			require.Equal(t, strings.TrimSpace(string(want)), strings.TrimSpace(string(got)))
		})
	}
}
