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
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func TestHMMCommandOnlyTurnUsesEligibleRequestedModel(t *testing.T) {
	const request = `{"model":"claude-opus-4-8","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>injected context</system-reminder>"},{"type":"text","text":"<command-name>local command</command-name>"},{"type":"text","text":"<local-command-stdout>local output</local-command-stdout>"}]}]}`
	env, err := translate.ParseAnthropic([]byte(request))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	require.False(t, hasTextUserBoundary(conversationMessagesForRouting(env)))

	sidecarFailure := errors.New("policy sidecar must not receive a command-only request")
	svc := buildPolicyDeadlineFallbackService(t, router.StrategyHMMEmbedding, sidecarFailure, nil, false, "")
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	requestForRouting := router.Request{
		RequestedModel:       features.Model,
		EstimatedInputTokens: features.Tokens,
		ConversationMessages: conversationMessagesForRouting(env),
	}
	turn, err := svc.runTurnLoop(ctx, env, features, "api-key", uuid.New(), "", http.Header{}, requestForRouting)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-8", turn.Decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, turn.Decision.Provider)
	assert.Equal(t, unscorableHMMDecisionReason, turn.Decision.Reason)
	assert.Equal(t, turn.Decision, turn.Fresh)

	// A real user message remains policy-scored, including a message carrying
	// both injected context and user-authored text.
	userRequest := router.Request{RequestedModel: features.Model, ConversationMessages: []router.ConversationMessage{{Role: "user", Text: "Please investigate"}}}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, userRequest)
	require.ErrorIs(t, err, sidecarFailure)
}

func TestHMMCommandOnlyTurnHonorsEligibilityAndPolicyPin(t *testing.T) {
	svc := buildPolicyDeadlineFallbackService(t, router.StrategyHMMEmbedding, errors.New("sidecar unavailable"), nil, false, "")
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	request := router.Request{RequestedModel: "claude-opus-4-8", ConversationMessages: []router.ConversationMessage{}}

	request.ExcludedModels = map[string]struct{}{"claude-opus-4-8": {}}
	_, err := svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, request)
	require.ErrorIs(t, err, policy.ErrNoRoutableModels)

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
	svc := buildPolicyDeadlineFallbackService(t, router.StrategyHMMEmbedding, errors.New("must not call sidecar"), store, false, "")
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
