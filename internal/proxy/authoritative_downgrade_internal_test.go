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

const authoritativeDowngradeStrategy = router.Strategy("authoritative-downgrade-test")

var authoritativeDowngradeBody = []byte(
	`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"fix the failing test"}]}`,
)

// authoritativeDowngradeService wires an authoritative policy whose fresh pick
// is fixed, over a pin store seeded with pin.
func authoritativeDowngradeService(store *stubPinStore, fresh router.Decision) *Service {
	return NewService(
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
		Strategy: authoritativeDowngradeStrategy,
		Router:   &authoritativeTestRouter{decision: fresh},
		Capabilities: policy.Capabilities{
			SchemaVersion:                 policy.SchemaVersionV1,
			AuthoritativePerTurnSelection: true,
		},
	})
}

func runAuthoritativeDowngradeTurn(t *testing.T, svc *Service) turnLoopResult {
	t.Helper()
	env, err := translate.ParseAnthropic(authoritativeDowngradeBody)
	require.NoError(t, err)
	features := env.RoutingFeatures(false)

	result, err := svc.runTurnLoop(
		router.WithStrategy(context.Background(), authoritativeDowngradeStrategy),
		env,
		features,
		"api-key",
		uuid.New(),
		"",
		http.Header{},
		router.Request{
			RequestedModel:       features.Model,
			ConversationMessages: conversationMessagesForRouting(env),
		},
	)
	require.NoError(t, err)
	assert.True(t, result.AuthoritativePerTurn)
	return result
}

func authoritativeDowngradePin(model string, votes int) sessionpin.Pin {
	return sessionpin.Pin{
		Provider:                  providers.ProviderAnthropic,
		Model:                     model,
		Reason:                    "hmm_policy(classifier 'maximum' (p=0.95))",
		PinnedUntil:               time.Now().Add(time.Hour),
		ConsecutiveDowngradeVotes: votes,
	}
}

func TestAuthoritativeDowngradeGuards(t *testing.T) {
	cheapFresh := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Reason:   "hmm_policy(classifier 'fast' (p=0.50))",
		Metadata: &router.RoutingMetadata{ChosenScore: 0.5},
	}
	confidentCheapFresh := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Reason:   "hmm_policy(classifier 'fast' (p=0.90))",
		Metadata: &router.RoutingMetadata{ChosenScore: 0.9},
	}
	cheapUpgradeFresh := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-4-8",
		Reason:   "hmm_policy(classifier 'maximum' (p=0.95))",
		Metadata: &router.RoutingMetadata{ChosenScore: 0.95},
	}

	tests := []struct {
		name            string
		pinModel        string
		priorVotes      int
		hysteresisTurns int
		downgradeGate   bool
		fresh           router.Decision
		wantModel       string
		wantSticky      bool
		wantTier        string
		wantVotes       int
	}{
		{
			name:      "both levers off downgrades on the first vote",
			pinModel:  "claude-opus-4-8",
			fresh:     cheapFresh,
			wantModel: "claude-haiku-4-5",
			wantTier:  "authoritative_per_turn",
			wantVotes: 0,
		},
		{
			name:            "first vote under hysteresis keeps the pin",
			pinModel:        "claude-opus-4-8",
			hysteresisTurns: 3,
			fresh:           cheapFresh,
			wantModel:       "claude-opus-4-8",
			wantSticky:      true,
			wantTier:        "authoritative_hmm_downgrade_hysteresis",
			wantVotes:       1,
		},
		{
			name:            "final vote under hysteresis serves fresh and clears the run",
			pinModel:        "claude-opus-4-8",
			priorVotes:      2,
			hysteresisTurns: 3,
			fresh:           cheapFresh,
			wantModel:       "claude-haiku-4-5",
			wantTier:        "authoritative_per_turn",
			wantVotes:       0,
		},
		{
			name:            "upgrade proposal ignores hysteresis and clears the run",
			pinModel:        "claude-haiku-4-5",
			priorVotes:      2,
			hysteresisTurns: 3,
			fresh:           cheapUpgradeFresh,
			wantModel:       "claude-opus-4-8",
			wantTier:        "authoritative_per_turn",
			wantVotes:       0,
		},
		{
			name:          "low-confidence downgrade keeps the pin without voting",
			pinModel:      "claude-opus-4-8",
			priorVotes:    1,
			downgradeGate: true,
			fresh:         cheapFresh,
			wantModel:     "claude-opus-4-8",
			wantSticky:    true,
			wantTier:      "authoritative_hmm_downgrade_confidence_low",
			wantVotes:     1,
		},
		{
			name:            "low-confidence downgrade is dropped before hysteresis counts it",
			pinModel:        "claude-opus-4-8",
			priorVotes:      2,
			hysteresisTurns: 3,
			downgradeGate:   true,
			fresh:           cheapFresh,
			wantModel:       "claude-opus-4-8",
			wantSticky:      true,
			wantTier:        "authoritative_hmm_downgrade_confidence_low",
			wantVotes:       2,
		},
		{
			name:          "confident downgrade clears the gate",
			pinModel:      "claude-opus-4-8",
			downgradeGate: true,
			fresh:         confidentCheapFresh,
			wantModel:     "claude-haiku-4-5",
			wantTier:      "authoritative_per_turn",
			wantVotes:     0,
		},
		{
			name:            "confident downgrade still serves its hysteresis sentence",
			pinModel:        "claude-opus-4-8",
			hysteresisTurns: 3,
			downgradeGate:   true,
			fresh:           confidentCheapFresh,
			wantModel:       "claude-opus-4-8",
			wantSticky:      true,
			wantTier:        "authoritative_hmm_downgrade_hysteresis",
			wantVotes:       1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newStubPinStore()
			store.getFound = true
			store.getPin = authoritativeDowngradePin(test.pinModel, test.priorVotes)
			svc := authoritativeDowngradeService(store, test.fresh).
				WithAuthoritativeDowngradeGate(test.downgradeGate).
				WithHMMDowngradeHysteresisTurns(test.hysteresisTurns)

			result := runAuthoritativeDowngradeTurn(t, svc)

			assert.Equal(t, test.wantModel, result.Decision.Model)
			assert.Equal(t, test.wantSticky, result.StickyHit)
			assert.Equal(t, test.wantTier, result.PinTier)
			require.Len(t, store.upserts, 1)
			assert.Equal(t, test.wantModel, store.upserts[0].Model)
			assert.Equal(t, test.wantVotes, store.upserts[0].ConsecutiveDowngradeVotes)
		})
	}
}

// TestAuthoritativeDowngradeHysteresisAcrossTurns walks the persisted counter
// through a run of cheaper votes broken by a confirming turn, which is the
// pattern the flag exists to suppress.
func TestAuthoritativeDowngradeHysteresisAcrossTurns(t *testing.T) {
	cheaper := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Reason:   "hmm_policy(classifier 'fast' (p=0.50))",
		Metadata: &router.RoutingMetadata{ChosenScore: 0.5},
	}
	confirming := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-4-8",
		Reason:   "hmm_policy(classifier 'maximum' (p=0.95))",
		Metadata: &router.RoutingMetadata{ChosenScore: 0.95},
	}

	turns := []struct {
		fresh     router.Decision
		wantModel string
		wantTier  string
		wantVotes int
	}{
		{fresh: cheaper, wantModel: "claude-opus-4-8", wantTier: "authoritative_hmm_downgrade_hysteresis", wantVotes: 1},
		{fresh: cheaper, wantModel: "claude-opus-4-8", wantTier: "authoritative_hmm_downgrade_hysteresis", wantVotes: 2},
		{fresh: confirming, wantModel: "claude-opus-4-8", wantTier: "authoritative_per_turn", wantVotes: 0},
		{fresh: cheaper, wantModel: "claude-opus-4-8", wantTier: "authoritative_hmm_downgrade_hysteresis", wantVotes: 1},
		{fresh: cheaper, wantModel: "claude-opus-4-8", wantTier: "authoritative_hmm_downgrade_hysteresis", wantVotes: 2},
		{fresh: cheaper, wantModel: "claude-haiku-4-5", wantTier: "authoritative_per_turn", wantVotes: 0},
	}

	pin := authoritativeDowngradePin("claude-opus-4-8", 0)
	for i, turn := range turns {
		store := newStubPinStore()
		store.getFound = true
		store.getPin = pin
		svc := authoritativeDowngradeService(store, turn.fresh).WithHMMDowngradeHysteresisTurns(3)

		result := runAuthoritativeDowngradeTurn(t, svc)

		assert.Equal(t, turn.wantModel, result.Decision.Model, "turn %d model", i+1)
		assert.Equal(t, turn.wantTier, result.PinTier, "turn %d tier", i+1)
		require.Len(t, store.upserts, 1)
		assert.Equal(t, turn.wantVotes, store.upserts[0].ConsecutiveDowngradeVotes, "turn %d votes", i+1)

		pin = store.upserts[0]
		pin.PinnedUntil = time.Now().Add(time.Hour)
	}
}
