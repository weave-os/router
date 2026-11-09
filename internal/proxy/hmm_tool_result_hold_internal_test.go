package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func TestHMMToolResultHoldKeepsUserTurnPick(t *testing.T) {
	const (
		pinnedModel = "claude-opus-4-7"
		freshModel  = "claude-opus-4-8"
	)
	toolResultBody := []byte(`{
		"model":"claude-opus-4-8",
		"tools":[{"name":"Read","description":"read","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"user","content":"inspect the repository"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"contents"}]}
		]
	}`)
	userTurnBody := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"fix the failing test"}]}`)
	tests := []struct {
		name              string
		body              []byte
		hold              bool
		wantModel         string
		wantAuthoritative bool
		wantRouteCalls    int
	}{
		{name: "tool result holds the session pin", body: toolResultBody, hold: true, wantModel: pinnedModel, wantRouteCalls: 0},
		{name: "tool result rescored without hold", body: toolResultBody, wantModel: freshModel, wantAuthoritative: true, wantRouteCalls: 1},
		{name: "user turn rescored with hold", body: userTurnBody, hold: true, wantModel: freshModel, wantAuthoritative: true, wantRouteCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			strategy := router.Strategy("hold-test")
			store := newStubPinStore()
			store.getFound = true
			store.getPin = sessionpin.Pin{
				Provider:        providers.ProviderAnthropic,
				Model:           pinnedModel,
				Reason:          "hold-test_policy",
				TurnCount:       3,
				PinnedUntil:     time.Now().Add(time.Hour),
				LastTurnEndedAt: time.Now().Add(-time.Minute),
				LastServedModel: pinnedModel,
			}
			policyRouter := &authoritativeTestRouter{decision: router.Decision{
				Provider: providers.ProviderAnthropic,
				Model:    freshModel,
				Reason:   "hold-test_policy",
			}}
			svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
				WithScoreToolResultTurns(true).
				WithHMMDowngradeHysteresisTurns(0).
				WithPolicyStrategy(policy.StrategySpec{
					Strategy: strategy,
					Router:   policyRouter,
					Capabilities: policy.Capabilities{
						SchemaVersion:                 policy.SchemaVersionV1,
						AuthoritativePerTurnSelection: true,
					},
				})
			env, err := translate.ParseAnthropic(test.body)
			require.NoError(t, err)
			features := env.RoutingFeatures(false)
			ctx := router.WithStrategy(context.Background(), strategy)
			ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyHMMToolResultHold: test.hold}})
			req := router.Request{
				RequestedModel:       features.Model,
				EstimatedInputTokens: features.Tokens,
				HasTools:             features.HasTools,
				ConversationMessages: conversationMessagesForRouting(env),
			}

			result, err := svc.runTurnLoop(ctx, env, features, "api-key", uuid.New(), "", http.Header{}, req)

			require.NoError(t, err)
			assert.Equal(t, test.wantModel, result.Decision.Model)
			assert.Equal(t, test.wantAuthoritative, result.AuthoritativePerTurn)
			assert.Len(t, policyRouter.requests, test.wantRouteCalls)
		})
	}
}
