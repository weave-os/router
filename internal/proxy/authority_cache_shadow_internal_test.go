package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/planner"
	"workweave/router/internal/router/policy"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"
)

const (
	shadowPinnedModel = "claude-opus-4-7"
	shadowFreshModel  = "claude-opus-4-6"
	shadowPinReason   = "hmm_policy(classifier 'high' (p=0.32))"
)

type authorityShadowTestRouter struct {
	decision router.Decision
}

func (r *authorityShadowTestRouter) Route(_ context.Context, _ router.Request) (router.Decision, error) {
	return r.decision, nil
}

func authorityShadowPin() sessionpin.Pin {
	return sessionpin.Pin{
		Provider:              providers.ProviderAnthropic,
		Model:                 shadowPinnedModel,
		Reason:                shadowPinReason,
		PolicyGroup:           "high",
		PinnedUntil:           time.Now().Add(time.Hour),
		LastTurnEndedAt:       time.Now().Add(-time.Minute),
		LastServedModel:       shadowPinnedModel,
		LastInputTokens:       4_000,
		LastCachedReadTokens:  12_000,
		LastCachedWriteTokens: 1_000,
		LastOutputTokens:      400,
	}
}

// runAuthorityShadowTurn drives one authoritative-per-turn main-loop turn and
// returns the turn loop's result.
func runAuthorityShadowTurn(t *testing.T, enabled bool, fresh router.Decision) turnLoopResult {
	t.Helper()
	strategy := router.Strategy("authority-cache-shadow-test")
	store := newStubPinStore()
	store.getFound = true
	store.getPin = authorityShadowPin()
	svc := NewService(
		nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithAuthorityCacheShadow(enabled).
		WithPlanner(planner.EVConfig{
			ThresholdUSD:           0.001,
			ExpectedRemainingTurns: 3,
		}).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: strategy,
			Router:   &authorityShadowTestRouter{decision: fresh},
			Capabilities: policy.Capabilities{
				SchemaVersion:                 policy.SchemaVersionV1,
				AuthoritativePerTurnSelection: true,
			},
		})

	env, err := translate.ParseAnthropic(
		[]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"continue"}]}`),
	)
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	ctx := router.WithStrategy(context.Background(), strategy)
	result, err := svc.runTurnLoop(
		ctx, env, features, "api-key", uuid.New(), "", http.Header{},
		router.Request{
			RequestedModel:       features.Model,
			EstimatedInputTokens: features.Tokens,
			HasTools:             features.HasTools,
			ConversationMessages: conversationMessagesForRouting(env),
			EnabledProviders:     map[string]struct{}{providers.ProviderAnthropic: {}},
		},
	)
	require.NoError(t, err)
	require.True(t, result.AuthoritativePerTurn)
	return result
}

func hmmFreshDecision(model string, scores map[string]float32) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    model,
		Reason:   "hmm_policy(classifier 'high' (p=0.41))",
		Metadata: &router.RoutingMetadata{
			Strategy:        "hmm",
			PolicyGroup:     "high",
			CandidateScores: scores,
		},
	}
}

// TestAuthorityCacheShadowNeverChangesTheServedDecision is the load-bearing
// guarantee: the shadow observes, it does not route. Production runs an
// authoritative sidecar, so a shadow that could alter dispatch would be a
// routing change shipped under the name of a measurement.
func TestAuthorityCacheShadowNeverChangesTheServedDecision(t *testing.T) {
	result := runAuthorityShadowTurn(t, true, hmmFreshDecision(shadowFreshModel, nil))

	assert.Equal(t, shadowFreshModel, result.Decision.Model, "served model must remain the authoritative fresh pick")
	assert.Equal(t, "authoritative_per_turn", result.PinTier)
	assert.False(t, result.StickyHit)
	// The generic planner still did not run; only the shadow did.
	assert.Empty(t, result.PlannerDecision.Reason)
	assert.True(t, result.AuthorityShadow.Computed)
}

// TestAuthorityCacheShadowRecordsTheAbandonedPin covers the column whose absence
// made over-switching unmeasurable: on a turn that serves fresh, the shadow must
// name the pin it priced against.
func TestAuthorityCacheShadowRecordsTheAbandonedPin(t *testing.T) {
	result := runAuthorityShadowTurn(t, true, hmmFreshDecision(shadowFreshModel, nil))

	shadow := result.AuthorityShadow
	require.True(t, shadow.Computed)
	assert.Equal(t, shadowPinnedModel, shadow.StayModel)
	assert.Equal(t, providers.ProviderAnthropic, shadow.StayProvider)
	assert.NotEqual(t, shadow.StayModel, result.Decision.Model)
	assert.NotEmpty(t, shadow.Decision.Reason)
}

// TestAuthorityCacheShadowKillSwitch proves the flag suppresses the computation
// outright rather than computing and discarding, so every column stays NULL.
func TestAuthorityCacheShadowKillSwitch(t *testing.T) {
	result := runAuthorityShadowTurn(t, false, hmmFreshDecision(shadowFreshModel, nil))

	assert.False(t, result.AuthorityShadow.Computed)
	assert.Equal(t, shadowFreshModel, result.Decision.Model)

	var params InsertTelemetryParams
	applyAuthorityShadowTelemetry(&params, result)
	assert.Empty(t, params.AuthorityShadowOutcome)
	assert.Nil(t, params.AuthorityShadowExpectedSavingsUSD)
	assert.Nil(t, params.AuthorityShadowPinCacheCold)
}

// TestAuthorityCacheShadowSkipsNonHMMDecisions keeps the shadow honest: the gate
// it previews only accepts HMM-written pins and reads a sidecar confidence, so a
// verdict against a non-HMM decision would describe a rollout that cannot happen.
func TestAuthorityCacheShadowSkipsNonHMMDecisions(t *testing.T) {
	result := runAuthorityShadowTurn(t, true, router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    shadowFreshModel,
		Reason:   "cluster:v0.2",
	})

	assert.False(t, result.AuthorityShadow.Computed)
	assert.Equal(t, shadowFreshModel, result.Decision.Model)
}

// TestAuthorityCacheShadowCandidateScoreCoverage locks the nil-vs-value contract
// on the scores. A missing score must stay nil rather than become 0.0: the NULL
// rate in production is the measurement that decides whether a quality tie-band
// can be built at all, and a stored zero would read as "scored, and terrible".
func TestAuthorityCacheShadowCandidateScoreCoverage(t *testing.T) {
	t.Run("both scored", func(t *testing.T) {
		result := runAuthorityShadowTurn(t, true, hmmFreshDecision(shadowFreshModel, map[string]float32{
			shadowPinnedModel: 0.62,
			shadowFreshModel:  0.71,
		}))
		shadow := result.AuthorityShadow
		require.NotNil(t, shadow.StayScore)
		require.NotNil(t, shadow.FreshScore)
		assert.InDelta(t, 0.62, *shadow.StayScore, 1e-6)
		assert.InDelta(t, 0.71, *shadow.FreshScore, 1e-6)
	})

	t.Run("pin absent from this turn's eligible arms", func(t *testing.T) {
		result := runAuthorityShadowTurn(t, true, hmmFreshDecision(shadowFreshModel, map[string]float32{
			shadowFreshModel: 0.71,
		}))
		shadow := result.AuthorityShadow
		assert.Nil(t, shadow.StayScore, "an unscored pin must persist as NULL, not 0.0")
		require.NotNil(t, shadow.FreshScore)
	})

	t.Run("sidecar reported no scores at all", func(t *testing.T) {
		result := runAuthorityShadowTurn(t, true, hmmFreshDecision(shadowFreshModel, nil))
		shadow := result.AuthorityShadow
		assert.Nil(t, shadow.StayScore)
		assert.Nil(t, shadow.FreshScore)
	})
}

// TestApplyAuthorityShadowTelemetryPreservesSignedEV guards the same trap
// migration 0066 was written around: a stay verdict routinely carries negative
// expected savings, and clamping it to zero destroys the measurement.
func TestApplyAuthorityShadowTelemetryPreservesSignedEV(t *testing.T) {
	stay := 0.71
	res := turnLoopResult{AuthorityShadow: authorityCacheShadow{
		Computed:     true,
		StayModel:    shadowPinnedModel,
		StayProvider: providers.ProviderAnthropic,
		Sticky:       true,
		StayScore:    &stay,
		Decision: planner.Decision{
			Outcome:                  planner.OutcomeStay,
			Reason:                   planner.ReasonEVNegative,
			ExpectedSavingsUSD:       -0.0025,
			EvictionCostUSD:          0.0091,
			PinCacheCold:             false,
			ShadowComputed:           true,
			ShadowOutcome:            planner.OutcomeSwitch,
			ShadowExpectedSavingsUSD: -0.028,
		},
	}}

	var params InsertTelemetryParams
	applyAuthorityShadowTelemetry(&params, res)

	assert.Equal(t, "stay", params.AuthorityShadowOutcome)
	assert.Equal(t, planner.ReasonEVNegative, params.AuthorityShadowReason)
	assert.Equal(t, shadowPinnedModel, params.AuthorityShadowStayModel)
	assert.Equal(t, providers.ProviderAnthropic, params.AuthorityShadowStayProvider)
	require.NotNil(t, params.AuthorityShadowExpectedSavingsUSD)
	assert.Negative(t, *params.AuthorityShadowExpectedSavingsUSD)
	require.NotNil(t, params.AuthorityShadowEvictionCostUSD)
	assert.Positive(t, *params.AuthorityShadowEvictionCostUSD)
	require.NotNil(t, params.AuthorityShadowPinCacheCold)
	assert.False(t, *params.AuthorityShadowPinCacheCold)
	// The corrected counterfactual comes free from planner.Decide and must be
	// stored separately from the deployed-config verdict, not merged into it.
	assert.Equal(t, "switch", params.AuthorityShadowCorrectedOutcome)
	require.NotNil(t, params.AuthorityShadowCorrectedSavingsUSD)
	assert.Negative(t, *params.AuthorityShadowCorrectedSavingsUSD)
	require.NotNil(t, params.AuthorityShadowStayScore)
	assert.InDelta(t, 0.71, *params.AuthorityShadowStayScore, 1e-6)
	assert.Nil(t, params.AuthorityShadowFreshScore)
}
