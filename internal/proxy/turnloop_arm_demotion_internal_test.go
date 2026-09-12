package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const (
	demotedPinModel = "claude-opus-4-7"
	freshTurnModel  = "claude-sonnet-5"
)

func demotionTurnLoopEnv(t *testing.T) (*translate.RequestEnvelope, translate.RoutingFeatures) {
	t.Helper()
	env, err := translate.ParseAnthropic(
		[]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"continue"}]}`),
	)
	require.NoError(t, err)
	return env, env.RoutingFeatures(false)
}

func runDemotionTurnLoop(t *testing.T, svc *Service, ctx context.Context) turnLoopResult {
	t.Helper()
	env, features := demotionTurnLoopEnv(t)
	res, err := svc.runTurnLoop(
		ctx,
		env,
		features,
		"api-key",
		uuid.New(),
		"",
		http.Header{},
		router.Request{
			RequestedModel:       features.Model,
			EstimatedInputTokens: features.Tokens,
			HasTools:             features.HasTools,
			ConversationMessages: conversationMessagesForRouting(env),
		},
	)
	require.NoError(t, err)
	return res
}

func demotedPin(model string, demoted ...string) sessionpin.Pin {
	return sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           model,
		Reason:          "cluster:v0.2",
		PinnedUntil:     time.Now().Add(time.Hour),
		LastTurnEndedAt: time.Now().Add(-time.Minute),
		LastServedModel: model,
		DemotedModels:   demoted,
	}
}

// The next turn after a committed-stream failure must leave the demoted arm:
// under an authoritative policy the exclusion has to reach the sidecar request
// itself, since that router — not the pin — picks the model every turn.
func TestTurnLoopExcludesDemotedModelUnderAuthoritativePolicy(t *testing.T) {
	strategy := router.Strategy("arm-demotion-authoritative-test")
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: demotedPin(demotedPinModel, demotedPinModel),
	}}
	policyRouter := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    freshTurnModel,
		Reason:   "arm-demotion-authoritative-test_policy",
	}}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: strategy,
			Router:   policyRouter,
			Capabilities: policy.Capabilities{
				SchemaVersion:                 policy.SchemaVersionV1,
				AuthoritativePerTurnSelection: true,
			},
		})

	res := runDemotionTurnLoop(t, svc, router.WithStrategy(context.Background(), strategy))

	assert.Equal(t, freshTurnModel, res.Decision.Model)
	require.Len(t, policyRouter.requests, 1)
	assert.Contains(t, policyRouter.requests[0].AutomaticExcludedModels, demotedPinModel,
		"the demoted arm must be withdrawn from the authoritative pick")
}

// On the scorer path the danger is the pin itself: without the exclusion the
// session stays glued to the arm that just died mid-stream.
func TestTurnLoopDropsDemotedPinOnScorerPath(t *testing.T) {
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: demotedPin(demotedPinModel, demotedPinModel),
	}}
	scorer := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    freshTurnModel,
		Reason:   "cluster:v0.2",
	}}
	svc := NewService(scorer, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	res := runDemotionTurnLoop(t, svc, context.Background())

	assert.Equal(t, freshTurnModel, res.Decision.Model)
	assert.False(t, res.StickyHit, "a demoted pin must not be reused")
	require.Len(t, scorer.requests, 1)
	assert.Contains(t, scorer.requests[0].AutomaticExcludedModels, demotedPinModel)
}

// HMM stickiness is recorded on the _hmm_history row, so a demotion written
// there has to count even when the base pin knows nothing about it.
func TestTurnLoopHonoursDemotionRecordedOnHMMHistory(t *testing.T) {
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole:                 demotedPin(demotedPinModel),
		hmmHistoryRole(sessionpin.DefaultRole): demotedPin(demotedPinModel, demotedPinModel),
	}}
	scorer := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    freshTurnModel,
		Reason:   "cluster:v0.2",
	}}
	svc := NewService(scorer, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	res := runDemotionTurnLoop(t, svc, context.Background())

	assert.Equal(t, freshTurnModel, res.Decision.Model)
	require.Len(t, scorer.requests, 1)
	assert.Contains(t, scorer.requests[0].AutomaticExcludedModels, demotedPinModel)
}

// /force-model is the escape hatch: an automatic strike-out must not silently
// revoke the model the user asked for by name.
func TestTurnLoopKeepsForcedModelDespiteDemotion(t *testing.T) {
	forced := demotedPin(demotedPinModel, demotedPinModel)
	forced.Reason = translate.ReasonUserForceModel
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{sessionpin.DefaultRole: forced}}
	scorer := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    freshTurnModel,
		Reason:   "cluster:v0.2",
	}}
	svc := NewService(scorer, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	res := runDemotionTurnLoop(t, svc, context.Background())

	assert.Equal(t, demotedPinModel, res.Decision.Model)
	assert.True(t, res.StickyHit)
	assert.Empty(t, scorer.requests, "a forced pin must not fall through to the scorer")
}
