package proxy

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type conditionalForceTestRouter struct {
	group   escalation.Group
	calls   int
	request router.Request
}

func (scorer *conditionalForceTestRouter) Route(_ context.Context, request router.Request) (router.Decision, error) {
	scorer.calls++
	scorer.request = request
	return router.Decision{
		Model: catalog.ModelIDClaudeHaiku45.String(), Provider: providers.ProviderAnthropic,
		Reason: "hmm_policy", Metadata: &router.RoutingMetadata{PolicyGroup: string(scorer.group), ClassifierArtifactID: "synthetic-classifier"},
	}, nil
}

func TestConditionalForceRoutesAndReplacesSingleRule(t *testing.T) {
	ctx := router.WithStrategy(context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "conditional-force-conversation"}), router.StrategyHMMEmbedding)
	installation := uuid.New()
	store := newForceModelMapStore()
	scorer := &conditionalForceTestRouter{group: escalation.Medium}
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: scorer})
	env, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"max_tokens":4096,"messages":[{"role":"user","content":"Implement a synthetic feature"}]}`, catalog.ModelIDClaudeOpus48.String())))
	require.NoError(t, err)
	const apiKey = "conditional-force-test"
	threadKey := deriveSessionKeyForRequest(ctx, env, apiKey)
	controlKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKey, threadKey)
	apply := func(command translate.ForceModelResult) commandAcknowledgment {
		t.Helper()
		immediate, ack, err := svc.applyForceModelCommand(ctx, command, installation, threadKey, controlKey)
		require.NoError(t, err)
		require.Equal(t, translate.CommandAccepted, ack.outcome.Disposition)
		if ack.outcome.Pin.Group != "" {
			assert.Empty(t, immediate, "a conditional command must not unconditionally force its continuation")
		}
		return ack
	}
	route := func() turnLoopResult {
		t.Helper()
		features := env.RoutingFeatures(false)
		turn, err := svc.runTurnLoop(ctx, env, features, apiKey, installation, "", nil, router.Request{RequestedModel: features.Model})
		require.NoError(t, err)
		return turn
	}
	apply(translate.ForceModelResult{Model: "opus-4-8"})
	ack := apply(translate.ForceModelResult{Model: "opus-4-8:high " + string(escalation.Medium)})
	assert.Equal(t, string(escalation.Medium), ack.outcome.Pin.Group)
	assert.Contains(t, ack.render(translate.FormatOpenAI), "only for medium")
	for _, group := range []escalation.Group{escalation.Medium, escalation.High, escalation.Low, escalation.Medium} {
		scorer.group = group
		turn := route()
		if group == escalation.Medium {
			assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), turn.Decision.Model)
			assert.Equal(t, "high", turn.Decision.Effort)
			assert.True(t, isUserForcedReason(turn.Decision.Reason))
			assert.Equal(t, "synthetic-classifier", turn.Decision.Metadata.ClassifierArtifactID)
		} else {
			assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), turn.Decision.Model)
			assert.Empty(t, turn.Decision.Effort)
		}
	}
	assert.Equal(t, 4, scorer.calls, "exactly one classification per scorable turn")
	automaticPin, found, err := store.Get(ctx, threadKey, roleForTier(catalog.TierFor(env.RoutingFeatures(false).Model)))
	require.NoError(t, err)
	require.True(t, found)
	automaticPin.DemotedModels = []string{catalog.ModelIDClaudeSonnet46.String()}
	require.NoError(t, store.Upsert(ctx, automaticPin))
	scorer.group = escalation.Low
	route()
	assert.Contains(t, scorer.request.AutomaticExcludedModels, catalog.ModelIDClaudeSonnet46.String(), "conditional scoring preserves session exclusions")
	assert.Equal(t, 5, scorer.calls)
	apply(translate.ForceModelResult{Model: "opus-4-8"})
	pin, found, err := store.Get(ctx, controlKey, forceModelSessionRole)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, pin.PolicyGroup, "an unconditional force of the same model clears the condition")
	assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), route().Decision.Model)
	assert.Equal(t, 5, scorer.calls, "unconditional forcing does not classify")
	apply(translate.ForceModelResult{Model: "opus-4-8 " + string(escalation.Medium)})
	svc.WithExcludedModelsOverride([]string{catalog.ModelIDClaudeOpus48.String()})
	scorer.group = escalation.Low
	assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), route().Decision.Model, "a nonmatching rule does not enforce its model's restrictions")
	scorer.group = escalation.Medium
	features := env.RoutingFeatures(false)
	_, err = svc.runTurnLoop(ctx, env, features, apiKey, installation, "", nil, router.Request{RequestedModel: features.Model})
	require.ErrorIs(t, err, ErrForcedModelExcluded)
	svc.WithExcludedModelsOverride(nil)
	scorer.group = ""
	_, err = svc.runTurnLoop(ctx, env, features, apiKey, installation, "", nil, router.Request{RequestedModel: features.Model})
	require.ErrorContains(t, err, "cannot classify")
	parentEnvelope := env
	env, err = translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"max_tokens":4096,"messages":[{"role":"user","content":"Implement a different synthetic child task"}]}`, catalog.ModelIDClaudeOpus48.String())))
	require.NoError(t, err)
	require.NotEqual(t, threadKey, deriveSessionKeyForRequest(ctx, env, apiKey))
	scorer.group = escalation.Medium
	assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), route().Decision.Model, "children inherit the same conditional rule")
	scorer.group = escalation.Low
	assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), route().Decision.Model, "a child's nonmatching turn routes automatically")
	env = parentEnvelope
	mainEnvelope := env
	env, err = translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"max_tokens":4096,"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"Implement a synthetic feature"},{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"synthetic file"}]}]}`, catalog.ModelIDClaudeOpus48.String())))
	require.NoError(t, err)
	scorer.group = escalation.Medium
	assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), route().Decision.Model, "tool results are classified rather than inheriting a sticky force")
	scorer.group = escalation.Low
	assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), route().Decision.Model)
	env = mainEnvelope
	scorer.group = escalation.High
	apply(translate.ForceModelResult{Model: "opus-4-8 " + string(escalation.High)})
	scorer.group = escalation.Medium
	assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), route().Decision.Model, "a new conditional force replaces, rather than accumulates with, the old group")
	scorer.group = escalation.High
	assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), route().Decision.Model)
	apply(translate.ForceModelResult{Clear: true})
	_, forced, cleared := svc.loadForceModelSessionPin(ctx, controlKey, threadKey, false)
	assert.False(t, forced)
	assert.True(t, cleared)
	assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), route().Decision.Model)
}

func TestConditionalForceWithLLMClassifier(t *testing.T) {
	fixture, principal, classifierStore := classifierSessionFixture(t, classifierMedium)
	scorer := &conditionalForceTestRouter{group: escalation.Medium}
	svc := NewService(nil, nil, nil, false, nil, newForceModelMapStore(), false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyLLMClassifier, Router: scorer})
	require.NoError(t, svc.WithClassifierSessions(fixture.classifierSessions.config, classifierStore, fixture.classifierSessions.classifier))
	ctx := classifierAdmit(t, svc, principal)
	ctx = router.WithStrategy(ctx, router.StrategyLLMClassifier)
	ctx = requestcontext.WithClientIdentity(ctx, ClientIdentity{SessionID: "synthetic-test-conversation"})
	body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":4096,"messages":[{"role":"user","content":"Implement a synthetic feature"}]}`, catalog.ModelIDClaudeOpus48.String()))
	ctx, err := svc.withClassifierInput(ctx, body, router.EndpointAnthropicMessages)
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	installation := installationIDFromContext(ctx)
	const apiKey = "test-credential"
	threadKey := deriveSessionKeyForRequest(ctx, env, apiKey)
	controlKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKey, threadKey)
	_, ack, err := svc.applyForceModelCommand(ctx, translate.ForceModelResult{Model: "opus-4-8 medium"}, installation, threadKey, controlKey)
	require.NoError(t, err)
	require.Equal(t, translate.CommandAccepted, ack.outcome.Disposition)
	features := env.RoutingFeatures(false)
	turn, err := svc.runTurnLoop(ctx, env, features, apiKey, installation, "", nil, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), turn.Decision.Model)
	require.NotNil(t, scorer.request.ClassifierPrediction)
	assert.Equal(t, router.ClassifierMedium, scorer.request.ClassifierPrediction.Complexity)
	assert.Equal(t, 1, scorer.calls)
}

func TestParseConditionalForceModel(t *testing.T) {
	for _, group := range []escalation.Group{escalation.Low, escalation.Medium, escalation.High, escalation.Maximum} {
		for _, separator := range []string{" ", " --group "} {
			model, parsedGroup, err := parseConditionalForceModel("opus:high" + separator + string(group))
			require.NoError(t, err)
			assert.Equal(t, "opus:high", model)
			assert.Equal(t, group, parsedGroup)
		}
	}
	for _, spec := range []string{"opus", "opus:medium", "opus 5.5"} {
		model, group, err := parseConditionalForceModel(spec)
		require.NoError(t, err)
		assert.Equal(t, spec, model)
		assert.Empty(t, group)
	}
	_, _, err := parseConditionalForceModel("opus --group typo")
	require.Error(t, err)
	_, _, err = parseConditionalForceModel("--group medium")
	require.Error(t, err)
}

func TestConditionalForceRejectsUnsupportedStrategyWithoutReplacingPin(t *testing.T) {
	ctx := router.WithStrategy(context.Background(), router.StrategyCluster)
	store := newForceModelMapStore()
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
	installation := uuid.New()
	key := [sessionpin.SessionKeyLen]byte{1}
	_, _, err := svc.applyForceModelCommand(ctx, translate.ForceModelResult{Model: "opus-4-8"}, installation, key, key)
	require.NoError(t, err)
	_, ack, err := svc.applyForceModelCommand(ctx, translate.ForceModelResult{Model: "opus medium"}, installation, key, key)
	require.NoError(t, err)
	assert.Equal(t, translate.CommandRejected, ack.outcome.Disposition)
	pin, found, _ := svc.loadForceModelSessionPin(ctx, key, key, false)
	require.True(t, found)
	assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), pin.Model)
	assert.Empty(t, pin.PolicyGroup)
}
