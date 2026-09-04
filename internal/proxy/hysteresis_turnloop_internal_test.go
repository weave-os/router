package proxy

import (
	"context"
	"net/http"
	"strings"
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

// The stored pin, not the fresh decision, carries the incumbent effort: the
// active-pin path reconstructs a decision without it, so hysteresis has to read
// the pin's served identity or a low-gap escalation silently re-prefixes the
// session.
func TestTurnLoopHoldsPinnedEffortOnLowGapEscalation(t *testing.T) {
	const pinnedModel = "claude-opus-4-7"
	strategy := router.Strategy("hmm-effort-hysteresis-test")

	tests := []struct {
		name       string
		highScore  float32
		wantEffort string
		wantHeld   bool
	}{
		{name: "low gap holds the pinned effort", highScore: 4.5, wantEffort: "low", wantHeld: true},
		{name: "gap above threshold escalates", highScore: 6.0, wantEffort: "high"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newStubPinStore()
			store.getFound = true
			store.getPin = sessionpin.Pin{
				Provider:        providers.ProviderAnthropic,
				Model:           pinnedModel,
				Reason:          "hmm_policy(classifier 'high' (p=0.32))",
				PolicyGroup:     "high",
				PinnedUntil:     time.Now().Add(time.Hour),
				LastTurnEndedAt: time.Now().Add(-time.Minute),
				LastServedModel: pinnedModel + ":low",
			}
			policyRouter := &authoritativeTestRouter{decision: router.Decision{
				Provider: providers.ProviderAnthropic,
				Model:    pinnedModel,
				Effort:   "high",
				Reason:   "hmm_policy(classifier 'high' (p=0.91))",
				Metadata: &router.RoutingMetadata{
					PolicyGroup:         "high",
					SelectedRosterArmID: "anthropic/" + pinnedModel + ":high",
					ArmScores: map[string]float32{
						"anthropic/" + pinnedModel + ":low":  4.0,
						"anthropic/" + pinnedModel + ":high": test.highScore,
					},
				},
			}}
			svc := NewService(
				nil,
				nil,
				nil,
				false,
				nil,
				store,
				false,
				providers.ProviderAnthropic,
				"claude-haiku-4-5",
				nil,
			).WithPolicyStrategy(policy.StrategySpec{
				Strategy:     strategy,
				Router:       policyRouter,
				Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1},
			})
			env, err := translate.ParseAnthropic(
				[]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"continue"}]}`),
			)
			require.NoError(t, err)
			features := env.RoutingFeatures(false)
			ctx := router.WithStrategy(context.Background(), strategy)

			result, err := svc.runTurnLoop(
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
			assert.Equal(t, pinnedModel, result.Decision.Model)
			assert.Equal(t, test.wantEffort, result.Decision.Effort)
			assert.Equal(t, test.wantHeld, strings.Contains(result.PlannerDecision.Reason, effortHysteresisReason),
				"a hold must stay legible in the planner reason")
		})
	}
}
