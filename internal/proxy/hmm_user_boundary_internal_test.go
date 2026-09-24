package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type unavailableHMMDecider struct{ err error }

func (d unavailableHMMDecider) Decide(context.Context, policy.Query) (policy.Result, error) {
	return policy.Result{}, d.err
}

func unscorableHMMService(store sessionpin.Store, sidecarFailure error) *Service {
	roster := &rosterdata.Roster{
		ClassOrder: []string{"low", "medium", "high", "maximum"},
		Clusters: map[string]rosterdata.Cluster{
			"low":     {Arms: []string{"anthropic/claude-haiku-4.5"}},
			"medium":  {Arms: []string{"anthropic/claude-sonnet-4.6"}},
			"maximum": {Arms: []string{"anthropic/claude-opus-5.5"}},
		},
	}
	policyRouter := hmm.NewForStrategy(router.StrategyHMMEmbedding, unavailableHMMDecider{sidecarFailure},
		map[string]struct{}{providers.ProviderAnthropic: {}})
	policyRouter.WithArmSelector(selection.Selector(roster))
	return NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: nil}, nil, false, nil,
		store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: policyRouter})
}

func TestHMMCommandOnlyTurnUsesEligibleRosterFallback(t *testing.T) {
	const request = `{"model":"claude-opus-4-8","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>injected context</system-reminder>"},{"type":"text","text":"<command-name>local command</command-name>"},{"type":"text","text":"<local-command-stdout>local output</local-command-stdout>"}]}]}`
	env, err := translate.ParseAnthropic([]byte(request))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	require.False(t, hasTextUserBoundary(conversationMessagesForRouting(env)))

	sidecarFailure := errors.New("policy sidecar must not receive a command-only request")
	svc := unscorableHMMService(nil, sidecarFailure)
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	requestForRouting := router.Request{
		RequestedModel:       features.Model,
		EstimatedInputTokens: features.Tokens,
		ConversationMessages: conversationMessagesForRouting(env),
	}
	turn, err := svc.runTurnLoop(ctx, env, features, "api-key", uuid.New(), "", http.Header{}, requestForRouting)
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", turn.Decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, turn.Decision.Provider)
	assert.Equal(t, policy.UnscorableHMMDecisionReason, turn.Decision.Reason)
	assert.Equal(t, turn.Decision, turn.Fresh)

	// A real user message remains policy-scored, including a message carrying
	// both injected context and user-authored text.
	userRequest := router.Request{RequestedModel: features.Model, ConversationMessages: []router.ConversationMessage{{Role: "user", Text: "Please investigate"}}}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, userRequest)
	require.ErrorIs(t, err, sidecarFailure)
}

func TestHMMCommandOnlyTurnHonorsEligibilityAndPolicyPin(t *testing.T) {
	svc := unscorableHMMService(nil, errors.New("sidecar unavailable"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	request := router.Request{RequestedModel: "claude-opus-5-5", ConversationMessages: []router.ConversationMessage{}}

	request.ExcludedModels = map[string]struct{}{"claude-opus-5-5": {}}
	decision, err := svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, request)
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", decision.Model)

	request.ExcludedModels = nil
	request.EnabledProviders = map[string]struct{}{providers.ProviderOpenAI: {}}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, request)
	require.ErrorIs(t, err, policy.ErrNoRoutableModels)

	request.EnabledProviders = nil
	pin := router.PolicyPin{ArtifactSHA256: "artifact", RosterSHA256: "roster"}
	ctx = router.WithPolicyPinRequest(ctx, router.PolicyPinRequest{Authorized: true, Pin: pin})
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, request)
	require.ErrorIs(t, err, router.ErrPolicyPinUnavailable)
}

func TestHMMCommandOnlyTurnUsesPromotedRosterAndAutomaticExclusions(t *testing.T) {
	svc := unscorableHMMService(nil, errors.New("must not call sidecar"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	req := router.Request{RequestedModel: "claude-opus-4-8", ConversationMessages: []router.ConversationMessage{}}
	decision, err := svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", decision.Model, "a catalog price does not admit a retired model to the serving roster")

	req.RequestedModel = "claude-opus-5-5"
	req.AutomaticExcludedModels = map[string]struct{}{"claude-opus-5-5": {}}
	decision, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.NotEqual(t, "claude-opus-5-5", decision.Model)
}

func TestHMMCommandOnlyEscalationRespectsSessionFloor(t *testing.T) {
	svc := unscorableHMMService(nil, errors.New("must not call sidecar"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	req := router.Request{
		RequestedModel:       "claude-haiku-4-5",
		ConversationMessages: []router.ConversationMessage{},
		PreviousPolicyGroup:  escalation.High,
		Escalation:           &escalation.Constraint{Floor: escalation.Medium},
	}
	decision, err := svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5", decision.Model)
	assert.Equal(t, "maximum", decision.Metadata.PolicyGroup)

	req.ExcludedModels = map[string]struct{}{"claude-opus-5-5": {}}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.ErrorIs(t, err, policy.ErrNoEligibleArm)
	require.ErrorIs(t, err, hmm.ErrHMMUnavailable)
	classified, matched := ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusServiceUnavailable, classified.Status)

	req.ExcludedModels = nil
	req.Escalation = &escalation.Constraint{Escalate: true}
	decision, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.Equal(t, "maximum", decision.Metadata.PolicyGroup)
}

func TestHMMCommandOnlyTurnHonorsForcedClusterAndKeyList(t *testing.T) {
	svc := unscorableHMMService(nil, errors.New("must not call sidecar"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	req := router.Request{
		RequestedModel:       "claude-haiku-4-5",
		ConversationMessages: []router.ConversationMessage{},
		ForceCluster:         "maximum",
		ClusterArmOverrides:  map[string][]string{"maximum": {"claude-opus-5-5"}},
	}
	decision, err := svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5", decision.Model)

	req.ClusterArmOverrides["maximum"] = []string{"claude-opus-4-8"}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.ErrorIs(t, err, policy.ErrForcedClusterUnservable)
	classified, matched := ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusBadRequest, classified.Status)

	req.ClusterArmOverrides = nil
	req.ForceCluster = "high"
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.ErrorIs(t, err, policy.ErrForcedClusterUnservable)
	classified, matched = ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusBadRequest, classified.Status)

	req.ForceCluster = "maximum"
	req.ClusterArmOverrides = map[string][]string{"maximum": {"claude-opus-5-5"}}
	req.ExcludedModels = map[string]struct{}{"claude-haiku-4-5": {}, "claude-sonnet-4-6": {}, "claude-opus-5-5": {}}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.ErrorIs(t, err, hmm.ErrHMMUnavailable)
	require.NotErrorIs(t, err, policy.ErrForcedClusterUnservable)
	classified, matched = ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusServiceUnavailable, classified.Status)
}

func TestHMMCommandOnlyTurnKeepsEligibleSessionPin(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":[{"type":"text","text":"<command-name>local command</command-name>"}]}]}`))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "claude-sonnet-4-6",
		Reason:      "hmm_policy",
		PinnedUntil: time.Now().Add(time.Hour),
	}
	svc := unscorableHMMService(store, errors.New("must not call sidecar"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	request := router.Request{RequestedModel: features.Model, ConversationMessages: conversationMessagesForRouting(env)}
	turn, err := svc.runTurnLoop(ctx, env, features, "api-key", uuid.New(), "", http.Header{}, request)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", turn.Decision.Model)
	assert.Equal(t, unscorableHMMStickyReason, turn.Decision.Reason)
	assert.True(t, turn.StickyHit)
	store.mu.Lock()
	defer store.mu.Unlock()
	require.NotEmpty(t, store.upserts)
	assert.Equal(t, "hmm_policy", store.upserts[len(store.upserts)-1].Reason)
}
