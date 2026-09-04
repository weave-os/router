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
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
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
	return authorityShadowPinServing(shadowPinnedModel)
}

// authorityShadowPinServing builds the pin with an explicit LastServedModel, so
// a test can exercise the effort-bearing serving identity ("model:effort") that
// Decision.ServedIdentity() persists.
func authorityShadowPinServing(servedIdentity string) sessionpin.Pin {
	pin := authorityShadowBasePin()
	pin.LastServedModel = servedIdentity
	return pin
}

func authorityShadowBasePin() sessionpin.Pin {
	return sessionpin.Pin{
		Provider:              providers.ProviderAnthropic,
		Model:                 shadowPinnedModel,
		Reason:                shadowPinReason,
		PolicyGroup:           "high",
		PinnedUntil:           time.Now().Add(time.Hour),
		LastTurnEndedAt:       time.Now().Add(-time.Minute),
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
	return runAuthorityShadowTurnWithPin(t, enabled, fresh, authorityShadowPin())
}

func runAuthorityShadowTurnWithPin(
	t *testing.T,
	enabled bool,
	fresh router.Decision,
	pin sessionpin.Pin,
) turnLoopResult {
	t.Helper()
	strategy := router.Strategy("authority-cache-shadow-test")
	store := newStubPinStore()
	store.getFound = true
	store.getPin = pin
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
	return hmmFreshDecisionWithArmScores(model, scores, nil)
}

func hmmFreshDecisionWithArmScores(
	model string,
	scores map[string]float32,
	armScores map[string]float32,
) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    model,
		Reason:   "hmm_policy(classifier 'high' (p=0.41))",
		Metadata: &router.RoutingMetadata{
			Strategy:        "hmm",
			PolicyGroup:     "high",
			CandidateScores: scores,
			ArmScores:       armScores,
		},
	}
}

// TestAuthorityCacheShadowNeverChangesTheServedDecision is the load-bearing
// guarantee: the shadow observes but does not route -- a shadow that could alter
// dispatch would be a routing change shipped under the name of a measurement.
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

	t.Run("AA sidecar arm scores", func(t *testing.T) {
		result := runAuthorityShadowTurn(t, true, hmmFreshDecisionWithArmScores(
			shadowFreshModel, nil, map[string]float32{
				"anthropic/" + shadowPinnedModel: 0.62,
				"anthropic/" + shadowFreshModel:  0.71,
			},
		))
		shadow := result.AuthorityShadow
		require.NotNil(t, shadow.StayScore)
		require.NotNil(t, shadow.FreshScore)
		assert.InDelta(t, 0.62, *shadow.StayScore, 1e-6)
		assert.InDelta(t, 0.71, *shadow.FreshScore, 1e-6)
	})

	t.Run("effort-qualified AA sidecar arm scores", func(t *testing.T) {
		const effort = "high"
		dec := hmmFreshDecisionWithArmScores(shadowFreshModel, nil, map[string]float32{
			"anthropic/" + shadowPinnedModel + ":" + effort: 0.62,
			"anthropic/" + shadowFreshModel + ":" + effort:  0.71,
		})
		stay := candidateScoreForWithProvider(dec, shadowPinnedModel+":"+effort, providers.ProviderAnthropic)
		fresh := candidateScoreForWithProvider(dec, shadowFreshModel+":"+effort, providers.ProviderAnthropic)
		require.NotNil(t, stay)
		require.NotNil(t, fresh)
		assert.InDelta(t, 0.62, *stay, 1e-6)
		assert.InDelta(t, 0.71, *fresh, 1e-6)
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

// TestAuthorityCacheShadowEffortBearingPinIsNotAStayCandidate documents a
// pre-existing property of hmmCostGatedDecision: effort-bearing pins land as
// no_pin because catalog.ByID strips date suffixes but not effort suffixes, so
// normalizeHMMStayPin rejects them. Not fixed here to avoid altering live HMM
// routing for self-hosters. Locked by test so analysts do not misread no_pin as
// "no pin existed".
func TestAuthorityCacheShadowEffortBearingPinIsNotAStayCandidate(t *testing.T) {
	const servedIdentity = shadowPinnedModel + ":high"

	result := runAuthorityShadowTurnWithPin(t, true,
		hmmFreshDecisionWithArmScores(shadowFreshModel,
			map[string]float32{shadowPinnedModel: 0.40, shadowFreshModel: 0.71},
			map[string]float32{servedIdentity: 0.66},
		),
		authorityShadowPinServing(servedIdentity),
	)

	require.True(t, result.AuthorityShadow.Computed)
	assert.Equal(t, planner.ReasonNoPin, result.AuthorityShadow.Reason(),
		"an effort-bearing pin does not resolve in the catalog, so the gate sees no pin")
	assert.Empty(t, result.AuthorityShadow.StayModel)
	assert.False(t, result.AuthorityShadow.Sticky)
	assert.Nil(t, result.AuthorityShadow.StayScore,
		"no stay candidate means no stay score, regardless of what the sidecar scored")
}

// TestCandidateScoreFor covers the lookup directly. CandidateScores is keyed
// by bare catalog ID; ArmScores uses roster arm IDs (provider/model[:effort]),
// so both namespaces are exercised with the nil-vs-value contract intact.
func TestCandidateScoreFor(t *testing.T) {
	dec := hmmFreshDecisionWithArmScores(shadowFreshModel,
		map[string]float32{shadowPinnedModel: 0.40, shadowFreshModel: 0.71},
		map[string]float32{"anthropic/" + shadowPinnedModel + ":high": 0.66},
	)

	t.Run("bare catalog id", func(t *testing.T) {
		got := candidateScoreFor(dec, shadowFreshModel)
		require.NotNil(t, got)
		assert.InDelta(t, 0.71, *got, 1e-6)
	})

	t.Run("AA arm score fallback", func(t *testing.T) {
		dec := hmmFreshDecisionWithArmScores(shadowFreshModel, nil, map[string]float32{
			"anthropic/" + shadowFreshModel: 0.71,
		})
		got := candidateScoreFor(dec, shadowFreshModel)
		require.NotNil(t, got)
		assert.InDelta(t, 0.71, *got, 1e-6)
	})

	t.Run("selected roster arm is authoritative for fresh score", func(t *testing.T) {
		const rosterArmID = "vendor/roster-alias-for-fresh"
		dec := hmmFreshDecisionWithArmScores(shadowFreshModel, nil, map[string]float32{
			rosterArmID: 0.71,
		})
		got := candidateScoreForWithArm(dec, shadowFreshModel, providers.ProviderAnthropic, rosterArmID)
		require.NotNil(t, got)
		assert.InDelta(t, 0.71, *got, 1e-6)
	})

	t.Run("stay score uses re-resolved provider", func(t *testing.T) {
		dec := hmmFreshDecisionWithArmScores(shadowFreshModel, nil, map[string]float32{
			"anthropic/" + shadowPinnedModel: 0.62,
			"openai/" + shadowPinnedModel:    0.91,
		})
		dec.Metadata.CandidateArmProviders = map[string]string{
			"anthropic/" + shadowPinnedModel: providers.ProviderAnthropic,
			"openai/" + shadowPinnedModel:    providers.ProviderOpenAI,
		}
		got := candidateScoreForWithProvider(dec, shadowPinnedModel, providers.ProviderAnthropic)
		require.NotNil(t, got)
		assert.InDelta(t, 0.62, *got, 1e-6)
	})

	t.Run("gateway provider falls back to roster namespace", func(t *testing.T) {
		dec := hmmFreshDecisionWithArmScores(shadowFreshModel, nil, map[string]float32{
			"anthropic/" + shadowFreshModel: 0.71,
		})
		got := candidateScoreForWithProvider(dec, shadowFreshModel, "anthropic_gateway")
		require.NotNil(t, got)
		assert.InDelta(t, 0.71, *got, 1e-6)
	})

	t.Run("effort suffix is stripped to the catalog id", func(t *testing.T) {
		got := candidateScoreFor(dec, shadowPinnedModel+":high")
		require.NotNil(t, got)
		assert.InDelta(t, 0.40, *got, 1e-6,
			"the model's score, not the roster arm's -- the namespaces differ")
	})

	t.Run("unscored model stays nil", func(t *testing.T) {
		assert.Nil(t, candidateScoreFor(dec, "claude-haiku-4-5"))
		assert.Nil(t, candidateScoreFor(dec, ""))
		assert.Nil(t, candidateScoreFor(router.Decision{}, shadowFreshModel))
	})
}

// TestAuthorityCacheShadowPersistsGateDivergenceVerdict locks the reason the
// divergence flag is a persisted column rather than a SQL string compare:
// stay_model carries ":effort" while decision_model is a bare catalog ID, so the
// string compare reports false divergence on every effort-bearing pin.
func TestAuthorityCacheShadowPersistsGateDivergenceVerdict(t *testing.T) {
	result := runAuthorityShadowTurn(t, true, hmmFreshDecision(shadowFreshModel, nil))

	var params InsertTelemetryParams
	applyAuthorityShadowTelemetry(&params, result)

	require.NotNil(t, params.AuthorityShadowWouldDiverge)
	assert.Equal(t, result.AuthorityShadow.Sticky, *params.AuthorityShadowWouldDiverge)
	assert.True(t, *params.AuthorityShadowWouldDiverge,
		"a stay verdict against a different served model is a divergence")
}

// TestAuthorityCacheShadowEarlyExitLeavesEVColumnsNull applies the 0066
// invariant to the early-exit paths: no_pin, no_prior_usage, same_model, and
// pricing_missing return before the cost arithmetic, so the EV fields stay zero
// and ShadowOutcome is the zero value (rendered "stay"). Persisting those as
// evidence would repeat the stored-zero-as-evidence failure.
func TestAuthorityCacheShadowEarlyExitLeavesEVColumnsNull(t *testing.T) {
	// An effort-bearing pin is rejected by normalizeHMMStayPin, so the gate takes
	// the no_pin early exit with no EV math.
	result := runAuthorityShadowTurnWithPin(t, true,
		hmmFreshDecision(shadowFreshModel, map[string]float32{shadowFreshModel: 0.71}),
		authorityShadowPinServing(shadowPinnedModel+":high"),
	)
	require.True(t, result.AuthorityShadow.Computed)
	require.False(t, result.AuthorityShadow.EVRan(), "no_pin returns before the EV block")

	var params InsertTelemetryParams
	applyAuthorityShadowTelemetry(&params, result)

	// The verdict itself is real evidence and must persist.
	assert.Equal(t, planner.ReasonNoPin, params.AuthorityShadowReason)
	require.NotNil(t, params.AuthorityShadowWouldDiverge)

	// Everything downstream of the cost arithmetic must not.
	assert.Nil(t, params.AuthorityShadowExpectedSavingsUSD)
	assert.Nil(t, params.AuthorityShadowEvictionCostUSD)
	assert.Nil(t, params.AuthorityShadowPinCacheCold)
	assert.Empty(t, params.AuthorityShadowCorrectedOutcome,
		"the zero Outcome renders as \"stay\"; an uncomputed verdict must stay NULL")
	assert.Nil(t, params.AuthorityShadowCorrectedSavingsUSD)
}
