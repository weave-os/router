package proxy

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/routingpolicy"
)

func TestWithDepsRoutingPrefixTrimFreeSwitch(t *testing.T) {
	prefixTrimFreeSwitchOff := false
	for name, tc := range map[string]struct {
		routing        *RoutingDeps
		wantPrefixTrim bool
		wantEffort     bool
	}{
		"nil routing keeps the defaults":       {routing: nil, wantPrefixTrim: true},
		"unset switch keeps prefix trim on":    {routing: &RoutingDeps{EffortEscalation: true}, wantPrefixTrim: true, wantEffort: true},
		"explicit false turns prefix trim off": {routing: &RoutingDeps{PrefixTrimFreeSwitch: &prefixTrimFreeSwitchOff}, wantPrefixTrim: false},
	} {
		t.Run(name, func(t *testing.T) {
			svc := WithDeps(NewService(nil, dispatch.NewClients(nil), nil, nil, false, "", "", nil), Deps{Routing: tc.routing})
			assert.Equal(t, tc.wantPrefixTrim, svc.routing.ResolvePrefixTrimFreeSwitch(context.Background()))
			assert.Equal(t, tc.wantEffort, svc.routing.ResolveEffortEscalation(context.Background()))
		})
	}
}

type depsEscalationStore struct{ escalation.Store }
type depsEscalationObserver struct{ escalation.Observer }
type depsLLMStore struct{ llmescalation.Store }
type depsJudge struct{ name string }
type depsClassifierStore struct{ router.ClassifierSessionStore }
type depsAtomicClassifier struct{ router.AtomicClassifier }
type depsStrategyRouter struct{ router.Router }
type depsExclusionStore struct{}

func (*depsJudge) Judge(context.Context, llmescalation.JudgeRequest) (llmescalation.Judgment, error) {
	return llmescalation.Judgment{}, nil
}

func (depsExclusionStore) ListGlobalAutomaticRoutingExclusions(context.Context) (map[string]string, error) {
	return nil, nil
}

func helperField(t *testing.T, h *floor.Helper, name string) reflect.Value {
	t.Helper()
	require.NotNil(t, h)
	field := reflect.ValueOf(h).Elem().FieldByName(name)
	require.True(t, field.IsValid(), name)
	return field
}

func newRoutingDepsService() *Service {
	return NewService(nil, dispatch.NewClients(nil), nil, nil, true, "anthropic", "claude-haiku-4-5", nil)
}

func TestWithDepsRoutingReachesService(t *testing.T) {
	ctx := context.Background()
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		map[string]struct{}{"claude-haiku-4-5": {}}, map[string]struct{}{"anthropic": {}}, func(m catalog.Model) string { return m.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)
	sessions, err := NewClassifierSessions(ClassifierSessionConfig{Release: "llm-classifier-v1.0.0", ReleaseSHA256: strings.Repeat("a", 64), SelectionPolicySHA256: strings.Repeat("b", 64), SigningKey: []byte(strings.Repeat("k", 32)), InstallationIDs: []uuid.UUID{uuid.New()}}, &depsClassifierStore{}, &depsAtomicClassifier{})
	require.NoError(t, err)
	prefixTrim := false
	continuityConfig := continuity.DefaultConfig()
	continuityConfig.PlannerEnabled = !continuityConfig.PlannerEnabled
	hmmConfig := continuity.DefaultAuthoritativeConfig()
	hmmConfig.UpgradeVotes += 3
	resolverCalled := false
	store, observer, llmStore, llmJudge, qwenJudge := &depsEscalationStore{}, &depsEscalationObserver{}, &depsLLMStore{}, &depsJudge{name: "switchyard"}, &depsJudge{name: "qwen"}

	svc := WithDeps(newRoutingDepsService(), Deps{Routing: &RoutingDeps{
		PrefixTrimFreeSwitch: &prefixTrim, EffortEscalation: true,
		AvailableModels: map[string]struct{}{"claude-haiku-4-5": {}}, DefaultBaselineModel: "claude-sonnet-5", ExcludedModels: []string{"gpt-5.5"},
		HardPinResolver:        func(HardPinRequest) (string, string, bool) { resolverCalled = true; return "", "", false },
		ExplicitUtilityHardPin: true, SubAgentProvider: "openai", SubAgentModel: "gpt-5.5",
		Deadline:                  routingpolicy.PolicyDeadline{Fallback: true, DefaultModel: "claude-sonnet-5"},
		GlobalAutomaticExclusions: depsExclusionStore{}, Plans: plans, Continuity: &continuityConfig, HMM: &hmmConfig, BandSwap: true,
		Strategies: []policy.StrategySpec{
			{Strategy: router.StrategyHMM, Router: &depsStrategyRouter{}, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1}},
			{Strategy: router.StrategyHMM, Router: &depsStrategyRouter{}, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV4}},
			{Strategy: router.StrategyDecisionModel, Router: &depsStrategyRouter{}},
		},
		Escalation:         &RoutingEscalation{Store: store, Observer: observer, LLMStore: llmStore, LLMJudge: llmJudge, QwenJudge: qwenJudge, ShadowDefault: flags.EscalationClassifierLLM},
		ClassifierSessions: sessions,
	}})

	assert.False(t, svc.routing.ResolvePrefixTrimFreeSwitch(ctx))
	assert.True(t, svc.routing.ResolveEffortEscalation(ctx))
	assert.Equal(t, map[string]struct{}{"claude-haiku-4-5": {}}, svc.routing.AvailableModels())
	assert.Equal(t, "claude-sonnet-5", svc.routing.DefaultBaselineModel())
	assert.Equal(t, map[string]struct{}{"gpt-5.5": {}}, svc.routing.ExcludedModelsOverride())
	pins := svc.routing.HardPins()
	assert.True(t, pins.Explore, "NewService hard-pin explore survives")
	assert.Equal(t, "anthropic", pins.Provider)
	assert.Equal(t, "claude-haiku-4-5", pins.Model)
	assert.True(t, pins.ExplicitUtility)
	assert.Equal(t, "openai", pins.SubAgentProvider)
	assert.Equal(t, "gpt-5.5", pins.SubAgentModel)
	require.NotNil(t, pins.Resolver)
	pins.Resolver(HardPinRequest{})
	assert.True(t, resolverCalled)
	assert.Equal(t, routingpolicy.PolicyDeadline{Fallback: true, DefaultModel: "claude-sonnet-5"}, svc.routing.Deadline())
	assert.NotNil(t, svc.routing.GlobalAutomaticExclusions())
	_, err = svc.routing.Plan(policy.RoutedResolutionRequest{})
	require.NotErrorIs(t, err, routingpolicy.ErrNoPlanResolver)
	assert.Equal(t, continuityConfig, svc.continuityConfig)
	assert.Equal(t, hmmConfig, svc.hmmConfig)
	assert.NotNil(t, svc.bandSwap)
	assert.Equal(t, []router.Strategy{router.StrategyDecisionModel, router.StrategyHMM}, svc.RegisteredStrategies())
	capabilities, ok := svc.PolicyCapabilities(router.StrategyHMM)
	require.True(t, ok)
	assert.Equal(t, policy.SchemaVersionV4, capabilities.SchemaVersion, "later strategy specs replace earlier ones, as chained WithPolicyStrategy calls did")
	assert.Same(t, sessions, svc.classifierSessions)
	assert.Equal(t, reflect.ValueOf(store).Pointer(), helperField(t, svc.escalation, "store").Elem().Pointer())
	assert.Equal(t, reflect.ValueOf(observer).Pointer(), helperField(t, svc.escalation, "observer").Elem().Pointer())
	assert.Equal(t, reflect.ValueOf(llmStore).Pointer(), helperField(t, svc.escalation, "llmStore").Elem().Pointer())
	assert.Equal(t, reflect.ValueOf(llmJudge).Pointer(), helperField(t, svc.escalation, "llmJudge").Elem().Pointer())
	assert.Equal(t, reflect.ValueOf(qwenJudge).Pointer(), helperField(t, svc.escalation, "qwenJudge").Elem().Pointer())
	assert.Equal(t, string(flags.EscalationClassifierLLM), helperField(t, svc.escalation, "shadowDefault").String())
	assert.False(t, helperField(t, svc.escalation, "slots").IsNil())
}

func TestWithDepsZeroRoutingKeepsNewServiceDefaults(t *testing.T) {
	ctx := context.Background()
	for name, routing := range map[string]*RoutingDeps{"nil": nil, "zero": {}} {
		t.Run(name, func(t *testing.T) {
			want := newRoutingDepsService()
			got := WithDeps(newRoutingDepsService(), Deps{Routing: routing})
			assert.True(t, got.routing.ResolvePrefixTrimFreeSwitch(ctx))
			assert.Equal(t, want.routing, got.routing)
			assert.Equal(t, want.continuityConfig, got.continuityConfig)
			assert.Equal(t, want.hmmConfig, got.hmmConfig)
			assert.Nil(t, got.bandSwap)
			assert.Empty(t, got.RegisteredStrategies())
			assert.Nil(t, got.classifierSessions)
			for _, field := range []string{"store", "observer", "llmStore", "llmJudge", "qwenJudge", "slots"} {
				assert.True(t, helperField(t, got.escalation, field).IsNil(), field)
			}
			assert.Equal(t, helperField(t, want.escalation, "shadowDefault").String(), helperField(t, got.escalation, "shadowDefault").String())
		})
	}
}
