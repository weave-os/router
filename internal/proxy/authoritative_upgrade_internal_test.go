package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

var (
	upgradeTestPinModel   = catalog.ModelIDClaudeHaiku45.String()
	upgradeTestFreshModel = catalog.ModelIDClaudeOpus48.String()
)

func TestEvaluateAuthoritativeUpgrade(t *testing.T) {
	margin, zero, invalid := 0.2, 0.0, math.NaN()
	qualified, unqualified := true, false
	votes := 3
	base := upgradeEvidence{
		HasPin: true, MoreExpensive: true, PricingKnown: true, FreshEligible: true,
		FreshGroup: escalation.High, PinGroup: escalation.High,
		Margin: &margin, MarginQualified: &unqualified,
	}
	for _, tc := range []struct {
		name   string
		change func(*upgradeEvidence)
		want   upgradeVerdict
	}{
		{"warm same group weak margin", func(e *upgradeEvidence) {}, upgradeVerdict{upgradeHold, upgradeWarmPinHold}},
		{"same group qualified", func(e *upgradeEvidence) { e.MarginQualified = &qualified }, upgradeVerdict{upgradeAllow, upgradeMarginQualified}},
		{"higher group qualified", func(e *upgradeEvidence) { e.FreshGroup = escalation.Maximum; e.MarginQualified = &qualified }, upgradeVerdict{upgradeAllow, upgradeGroupEscalation}},
		{"higher group weak", func(e *upgradeEvidence) { e.FreshGroup = escalation.Maximum }, upgradeVerdict{upgradeHold, upgradeInsufficientMargin}},
		{"cold pin", func(e *upgradeEvidence) { e.CacheCold = true }, upgradeVerdict{upgradeAllow, upgradeColdPin}},
		{"prefix broken", func(e *upgradeEvidence) { e.PrefixBroken = true; e.CacheCold = true }, upgradeVerdict{upgradeAllow, upgradePrefixBroken}},
		{"calibrated stale pin", func(e *upgradeEvidence) { e.StalePin = true }, upgradeVerdict{upgradeAllow, upgradeStalePin}},
		{"explicit high floor", func(e *upgradeEvidence) { e.RequestedGroup = escalation.High; e.PinGroup = escalation.Medium }, upgradeVerdict{upgradeAllow, upgradeExplicitQualityFloor}},
		{"explicit maximum floor", func(e *upgradeEvidence) { e.RequestedGroup = escalation.Maximum; e.FreshGroup = escalation.Maximum }, upgradeVerdict{upgradeAllow, upgradeExplicitQualityFloor}},
		{"fresh does not meet requested floor", func(e *upgradeEvidence) { e.RequestedGroup = escalation.Maximum }, upgradeVerdict{upgradeHold, upgradeWarmPinHold}},
		{"unknown requested group is not maximum", func(e *upgradeEvidence) { e.RequestedGroup = escalation.Group("future") }, upgradeVerdict{upgradeHold, upgradeWarmPinHold}},
		{"missing fresh group", func(e *upgradeEvidence) { e.FreshGroup = "" }, upgradeVerdict{upgradeExistingPolicy, upgradeMissingGroup}},
		{"unknown pin group even when cold", func(e *upgradeEvidence) { e.PinGroup = escalation.Group("future"); e.CacheCold = true }, upgradeVerdict{upgradeExistingPolicy, upgradeMissingGroup}},
		{"missing margin", func(e *upgradeEvidence) { e.Margin = nil }, upgradeVerdict{upgradeExistingPolicy, upgradeMissingMargin}},
		{"invalid margin", func(e *upgradeEvidence) { e.Margin = &invalid }, upgradeVerdict{upgradeExistingPolicy, upgradeInvalidMargin}},
		{"zero margin is evidence", func(e *upgradeEvidence) { e.Margin = &zero }, upgradeVerdict{upgradeHold, upgradeWarmPinHold}},
		{"uncalibrated margin", func(e *upgradeEvidence) { e.MarginQualified = nil }, upgradeVerdict{upgradeExistingPolicy, upgradeUncalibratedMargin}},
		{"repeated compatible votes", func(e *upgradeEvidence) { e.VoteCount = &votes; e.RequiredVotes = 3 }, upgradeVerdict{upgradeAllow, upgradeRepeatedVote}},
		{"votes below threshold", func(e *upgradeEvidence) { e.VoteCount = &votes; e.RequiredVotes = 4 }, upgradeVerdict{upgradeHold, upgradeWarmPinHold}},
		{"votes unavailable", func(e *upgradeEvidence) { e.RequiredVotes = 3 }, upgradeVerdict{upgradeHold, upgradeWarmPinHold}},
		{"votes cannot rescue missing evidence", func(e *upgradeEvidence) { e.VoteCount = &votes; e.RequiredVotes = 3; e.Margin = nil }, upgradeVerdict{upgradeExistingPolicy, upgradeMissingMargin}},
		{"demotion precedes cold floor votes", func(e *upgradeEvidence) {
			e.Demoted = true
			e.CacheCold = true
			e.RequestedGroup = escalation.Maximum
			e.VoteCount = &votes
			e.RequiredVotes = 3
		}, upgradeVerdict{upgradeExcluded, upgradeDemotedModel}},
		{"unavailable fresh arm", func(e *upgradeEvidence) { e.FreshEligible = false }, upgradeVerdict{upgradeExcluded, upgradeIneligibleModel}},
		{"no pin", func(e *upgradeEvidence) { e.HasPin = false }, upgradeVerdict{upgradeNotApplicable, upgradeNoPin}},
		{"same model", func(e *upgradeEvidence) { e.SameModel = true }, upgradeVerdict{upgradeNotApplicable, upgradeSameModel}},
		{"missing pricing", func(e *upgradeEvidence) { e.PricingKnown = false }, upgradeVerdict{upgradeExistingPolicy, upgradeMissingPricing}},
		{"cheaper proposal", func(e *upgradeEvidence) { e.MoreExpensive = false }, upgradeVerdict{upgradeNotApplicable, upgradeNotMoreExpensive}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := base
			tc.change(&evidence)
			assert.Equal(t, tc.want, evaluateAuthoritativeUpgrade(evidence))
		})
	}
}

func upgradeTestPin() sessionpin.Pin {
	return sessionpin.Pin{
		Model: upgradeTestPinModel, Provider: providers.ProviderAnthropic,
		Reason: "hmm_policy", PolicyGroup: string(escalation.Medium),
		PinnedUntil: time.Now().Add(time.Hour), FirstPinnedAt: time.Now().Add(-2 * time.Hour),
		LastTurnEndedAt: time.Now().Add(-time.Minute), LastOutputTokens: 100,
	}
}

func upgradeTestFresh() router.Decision {
	margin := 0.2
	return router.Decision{
		Model: upgradeTestFreshModel, Provider: providers.ProviderAnthropic, Reason: "hmm_policy",
		Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMM), PolicyGroup: string(escalation.High), ChosenScore: 0.7, ClassifierMargin: &margin},
	}
}

func TestUpgradeShadowUsesCacheRulesAndExplicitIntent(t *testing.T) {
	margin := 0.2
	svc := (&Service{}).WithAuthorityCacheShadow(true).WithAuthoritativeUpgradeGate(true).
		WithAuthoritativeUpgradeConfig(AuthoritativeUpgradeConfig{MarginThreshold: &margin})
	pin, fresh := upgradeTestPin(), upgradeTestFresh()
	req := router.Request{RequestedModel: upgradeTestFreshModel, RoutingIntent: string(escalation.Maximum)}
	res := turnLoopResult{RequestedTier: catalog.TierHigh}
	shadow := svc.authoritativeUpgradeFor(context.Background(), req, pin, fresh, res, 1000)
	require.NotNil(t, shadow)
	assert.Equal(t, upgradeGroupEscalation, shadow.Verdict.Reason)
	assert.Empty(t, shadow.Evidence.RequestedGroup, "neither a model name nor an installation preset proves explicit intent")
	require.NotNil(t, shadow.Evidence.VoteCount)
	assert.Zero(t, *shadow.Evidence.VoteCount)
	require.NotNil(t, shadow.PinAgeSec)
	assert.InDelta(t, 7200, *shadow.PinAgeSec, 2)
	assert.False(t, shadow.Evidence.StalePin, "pin age alone cannot arm an uncalibrated escape hatch")
	require.NotNil(t, shadow.Evidence.MarginQualified)
	assert.True(t, *shadow.Evidence.MarginQualified, "threshold comparison is inclusive")

	req.ForceCluster = string(escalation.High)
	shadow = svc.authoritativeUpgradeFor(context.Background(), req, pin, fresh, res, 1000)
	assert.Equal(t, upgradeExplicitQualityFloor, shadow.Verdict.Reason)

	req.HistoryTruncated = true
	shadow = svc.authoritativeUpgradeFor(context.Background(), req, pin, fresh, res, 1000)
	assert.Equal(t, upgradePrefixBroken, shadow.Verdict.Reason)
	assert.True(t, shadow.Evidence.CacheCold)
	req.HistoryTruncated = false
	pin.LastTurnEndedAt = time.Now().Add(-providers.CacheTTLFor(pin.Provider) - time.Minute)
	shadow = svc.authoritativeUpgradeFor(context.Background(), req, pin, fresh, res, 1000)
	assert.Equal(t, upgradeColdPin, shadow.Verdict.Reason)

	res.SessionDemotedModels = []string{upgradeTestFreshModel}
	shadow = svc.authoritativeUpgradeFor(context.Background(), req, pin, fresh, res, 1000)
	assert.Equal(t, upgradeDemotedModel, shadow.Verdict.Reason)
	res.SessionDemotedModels = nil
	req.AutomaticExcludedModels = map[string]struct{}{upgradeTestFreshModel: {}}
	shadow = svc.authoritativeUpgradeFor(context.Background(), req, pin, fresh, res, 1000)
	assert.Equal(t, upgradeIneligibleModel, shadow.Verdict.Reason)
}

func TestUpgradeShadowAgeAndAbsentPin(t *testing.T) {
	svc := (&Service{}).WithAuthorityCacheShadow(true).
		WithAuthoritativeUpgradeConfig(AuthoritativeUpgradeConfig{StalePinAfter: time.Hour})
	pin, fresh := upgradeTestPin(), upgradeTestFresh()
	shadow := svc.authoritativeUpgradeFor(context.Background(), router.Request{}, pin, fresh, turnLoopResult{}, 1000)
	assert.Equal(t, upgradeStalePin, shadow.Verdict.Reason)
	assert.Equal(t, flags.AuthoritativeUpgradePolicyOff, shadow.Mode)

	pin.FirstPinnedAt = time.Now().Add(-30 * time.Minute)
	shadow = svc.authoritativeUpgradeFor(context.Background(), router.Request{}, pin, fresh, turnLoopResult{}, 1000)
	assert.False(t, shadow.Evidence.StalePin)
	assert.Equal(t, upgradeUncalibratedMargin, shadow.Verdict.Reason)

	pin.FirstPinnedAt = time.Time{}
	shadow = svc.authoritativeUpgradeFor(context.Background(), router.Request{}, pin, fresh, turnLoopResult{}, 1000)
	assert.Nil(t, shadow.PinAgeSec)
	assert.False(t, shadow.Evidence.StalePin)

	shadow = svc.authoritativeUpgradeFor(context.Background(), router.Request{}, sessionpin.Pin{}, fresh, turnLoopResult{}, 1000)
	assert.Equal(t, upgradeNoPin, shadow.Verdict.Reason)
	assert.Nil(t, shadow.PinAgeSec)
}

func TestUpgradeShadowPreservesServingAndPinWrites(t *testing.T) {
	for _, tc := range []struct {
		name      string
		score     float32
		gate      bool
		cold      bool
		wantModel string
	}{
		{"warm blocked upgrade", 0.7, true, false, upgradeTestPinModel},
		{"cold blocked upgrade", 0.7, true, true, upgradeTestPinModel},
		{"confident upgrade", 0.91, true, false, upgradeTestFreshModel},
		{"unscored upgrade", 0, true, false, upgradeTestFreshModel},
		{"gate off", 0.7, false, false, upgradeTestFreshModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var control turnLoopResult
			var controlPin sessionpin.Pin
			for _, enabled := range []bool{false, true} {
				store := newStubPinStore()
				store.getFound = true
				store.getPin = upgradeTestPin()
				if tc.cold {
					store.getPin.LastTurnEndedAt = time.Time{}
				}
				fresh := upgradeTestFresh()
				fresh.Metadata.ChosenScore = tc.score
				margin := 0.1
				strategy := router.Strategy("upgrade-shadow-test")
				svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, upgradeTestPinModel, nil).
					WithAuthorityCacheShadow(enabled).WithAuthoritativeUpgradeGate(tc.gate).
					WithAuthoritativeUpgradeConfig(AuthoritativeUpgradeConfig{MarginThreshold: &margin}).
					WithPolicyStrategy(policy.StrategySpec{Strategy: strategy, Router: &authorityShadowTestRouter{decision: fresh}, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
				env, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"continue"}]}`, catalog.ModelIDClaudeOpus48.String())))
				require.NoError(t, err)
				feats := env.RoutingFeatures(false)
				res, err := svc.runTurnLoop(router.WithStrategy(context.Background(), strategy), env, feats, "test-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: feats.Model})
				require.NoError(t, err)
				assert.Equal(t, tc.wantModel, res.Decision.Model)
				require.Len(t, store.upserts, 1)
				require.NotNil(t, res.UpgradeShadow)
				if !enabled {
					control, controlPin = res, store.upserts[0]
					continue
				}
				assert.Equal(t, upgradeAllow, res.UpgradeShadow.Verdict.Outcome)
				assert.Equal(t, control.Decision, res.Decision)
				assert.Equal(t, control.StickyHit, res.StickyHit)
				assert.Equal(t, control.PinTier, res.PinTier)
				assert.Equal(t, control.PlannerDecision, res.PlannerDecision)
				assert.Equal(t, controlPin.Model, store.upserts[0].Model)
				assert.Equal(t, controlPin.Provider, store.upserts[0].Provider)
				assert.Equal(t, controlPin.Reason, store.upserts[0].Reason)
				assert.Equal(t, controlPin.PolicyGroup, store.upserts[0].PolicyGroup)
				assert.Zero(t, store.upserts[0].ConsecutiveDowngradeVotes)
				if res.StickyHit {
					assert.Nil(t, res.Decision.Metadata, "replayed pins must not re-emit fresh margin")
				}
			}
		})
	}
}

func TestUpgradeShadowLogsNullableEvidence(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))
	fresh, pin := upgradeTestFresh(), upgradeTestPin()
	svc := (&Service{}).WithAuthorityCacheShadow(true).WithAuthoritativeUpgradeGate(true)
	res := turnLoopResult{Fresh: fresh, Decision: pinDecision(pin), StickyHit: true}
	res.UpgradeShadow = svc.authoritativeUpgradeFor(ctx, router.Request{}, pin, fresh, res, 1000)
	logAuthoritativeUpgrade(ctx, res)
	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	assert.Equal(t, string(upgradeExistingPolicy), record["outcome"])
	assert.Equal(t, string(upgradeUncalibratedMargin), record["reason"])
	assert.Equal(t, 0.2, record["margin"])
	assert.Equal(t, float64(0), record["vote_count"])
	assert.Nil(t, record["margin_threshold"])
	assert.Nil(t, record["would_diverge"], "unknown evidence is not a measured non-divergence")
	assert.NotContains(t, record, "shadow_expected_savings_usd")
	assert.Equal(t, upgradeTestPinModel, record["served_model"])
}

func TestUpgradeShadowCapturesMarginOnStayButNotReplay(t *testing.T) {
	fresh := upgradeTestFresh()
	zero := 0.0
	fresh.Metadata.ClassifierMargin = &zero
	served := pinDecision(upgradeTestPin())
	obs := buildObservationContext(context.Background(), served, fresh, CaptureOff)
	assert.Equal(t, &zero, obs.FreshClassifierMargin)
	assert.Equal(t, string(escalation.High), obs.FreshPolicyGroup)
	builder := otel.NewAttrBuilder(4)
	obs.applySpanAttrs(builder)
	found := false
	for _, attr := range builder.Build() {
		if attr.Key == "routing.fresh_classifier_margin" {
			found = true
			assert.Equal(t, 0.0, attr.Value.GetDoubleValue())
		}
	}
	assert.True(t, found)
	replay := buildObservationContext(context.Background(), served, router.Decision{}, CaptureOff)
	assert.Nil(t, replay.FreshClassifierMargin)
	assert.Empty(t, replay.FreshPolicyGroup)
}

func TestUpgradeShadowPreservesHigherPrecedencePaths(t *testing.T) {
	for _, tc := range []struct {
		name       string
		forced     bool
		compaction bool
		deadline   bool
		escalated  bool
		wantModel  string
	}{
		{name: "forced pin", forced: true, wantModel: upgradeTestPinModel},
		{name: "compaction hard pin", compaction: true, wantModel: upgradeTestPinModel},
		{name: "policy deadline fallback", deadline: true, wantModel: upgradeTestPinModel},
		{name: "active escalation floor", escalated: true, wantModel: upgradeTestFreshModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newStubPinStore()
			store.getFound = true
			store.getPin = upgradeTestPin()
			if tc.forced {
				store.getPin.Reason = translate.ReasonUserForceModel
			}
			fresh := upgradeTestFresh()
			if tc.escalated {
				fresh.Metadata.Escalation = &escalation.Decision{Constrained: true, Effective: escalation.High, Outcome: escalation.OutcomeFloor}
			}
			var policyRouter router.Router = &authorityShadowTestRouter{decision: fresh}
			if tc.deadline {
				policyRouter = &erroringTestRouter{err: policyDeadlineTestErr}
			}
			strategy := router.Strategy("upgrade-precedence-test")
			svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, upgradeTestPinModel, nil).
				WithAuthorityCacheShadow(true).WithPolicyDeadlineFallback(true).
				WithPolicyStrategy(policy.StrategySpec{Strategy: strategy, Router: policyRouter, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"continue"}]}`, catalog.ModelIDClaudeOpus48.String())
			if tc.compaction {
				body = fmt.Sprintf(`{"model":%q,"system":"Your task is to create a detailed summary of the conversation so far.","messages":[{"role":"user","content":"summarize"}]}`, catalog.ModelIDClaudeOpus48.String())
			}
			env, err := translate.ParseAnthropic([]byte(body))
			require.NoError(t, err)
			feats := env.RoutingFeatures(false)
			res, err := svc.runTurnLoop(router.WithStrategy(context.Background(), strategy), env, feats, "test-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: feats.Model})
			require.NoError(t, err)
			assert.Equal(t, tc.wantModel, res.Decision.Model)
			assert.Nil(t, res.UpgradeShadow)
			assert.Equal(t, tc.compaction, res.HardPinned)
			assert.Equal(t, tc.deadline, res.PolicyFallback)
		})
	}
}

func TestEvidenceUpgradeServesFreshWhenQualified(t *testing.T) {
	store := newStubPinStore()
	store.getFound = true
	store.getPin = upgradeTestPin()
	fresh := upgradeTestFresh()
	fresh.Metadata.ChosenScore = 0.7
	margin := 0.2
	threshold := 0.15
	fresh.Metadata.ClassifierMargin = &margin
	strategy := router.Strategy("upgrade-evidence-test")
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, upgradeTestPinModel, nil).
		WithAuthoritativeUpgradeGate(true).
		WithAuthoritativeUpgradePolicy(flags.AuthoritativeUpgradePolicyEvidence).
		WithAuthoritativeUpgradeVotes(3).
		WithAuthoritativeUpgradeConfig(AuthoritativeUpgradeConfig{MarginThreshold: &threshold}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: strategy, Router: &authorityShadowTestRouter{decision: fresh}, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
	env, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"continue"}]}`, catalog.ModelIDClaudeOpus48.String())))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(router.WithStrategy(context.Background(), strategy), env, feats, "test-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: feats.Model})
	require.NoError(t, err)
	assert.Equal(t, upgradeTestFreshModel, res.Decision.Model)
	assert.False(t, res.StickyHit)
	require.NotNil(t, res.UpgradeShadow)
	assert.True(t, res.UpgradeShadow.Applied)
	assert.Equal(t, upgradeAllow, res.UpgradeShadow.Verdict.Outcome)
	assert.Equal(t, upgradeGroupEscalation, res.UpgradeShadow.Verdict.Reason)
}

func TestEvidenceUpgradeHoldsWarmLowMarginAndCountsVotes(t *testing.T) {
	store := newStubPinStore()
	store.getFound = true
	store.getPin = upgradeTestPin()
	fresh := upgradeTestFresh()
	fresh.Metadata.PolicyGroup = string(escalation.Medium)
	fresh.Metadata.ChosenScore = 0.91
	margin := 0.05
	threshold := 0.15
	fresh.Metadata.ClassifierMargin = &margin
	strategy := router.Strategy("upgrade-evidence-hold")
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, upgradeTestPinModel, nil).
		WithAuthoritativeUpgradeGate(true).
		WithAuthoritativeUpgradePolicy(flags.AuthoritativeUpgradePolicyEvidence).
		WithAuthoritativeUpgradeVotes(3).
		WithAuthoritativeUpgradeConfig(AuthoritativeUpgradeConfig{MarginThreshold: &threshold}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: strategy, Router: &authorityShadowTestRouter{decision: fresh}, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
	env, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"continue"}]}`, catalog.ModelIDClaudeOpus48.String())))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(router.WithStrategy(context.Background(), strategy), env, feats, "test-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: feats.Model})
	require.NoError(t, err)
	assert.Equal(t, upgradeTestPinModel, res.Decision.Model)
	assert.True(t, res.StickyHit)
	require.NotNil(t, res.UpgradeShadow)
	assert.Equal(t, upgradeHold, res.UpgradeShadow.Verdict.Outcome)
	require.Len(t, store.upserts, 1)
	assert.Equal(t, 1, store.upserts[0].ConsecutiveUpgradeVotes)
}

func TestEvidenceUpgradeVotesResetWhenProposalLeavesSameGroup(t *testing.T) {
	margin := 0.05
	threshold := 0.15
	svc := (&Service{}).WithAuthoritativeUpgradeConfig(AuthoritativeUpgradeConfig{MarginThreshold: &threshold})
	pin := upgradeTestPin()
	pin.ConsecutiveUpgradeVotes = 2
	fresh := upgradeTestFresh()
	fresh.Metadata.ClassifierMargin = &margin

	shadow := svc.authoritativeUpgradeFor(context.Background(), router.Request{}, pin, fresh, turnLoopResult{}, 1000)
	assert.Equal(t, upgradeHold, shadow.Verdict.Outcome)
	assert.Equal(t, escalation.High, shadow.Evidence.FreshGroup)
	assert.Equal(t, escalation.Medium, shadow.Evidence.PinGroup)
	require.NotNil(t, shadow.Evidence.VoteCount)
	assert.Zero(t, *shadow.Evidence.VoteCount, "a different-group hold breaks vote consecutiveness")

	pin.ConsecutiveUpgradeVotes = *shadow.Evidence.VoteCount
	fresh.Metadata.PolicyGroup = string(escalation.Medium)
	shadow = svc.authoritativeUpgradeFor(context.Background(), router.Request{}, pin, fresh, turnLoopResult{}, 1000)
	require.NotNil(t, shadow.Evidence.VoteCount)
	assert.Equal(t, 1, *shadow.Evidence.VoteCount)
}

func TestEvidenceUpgradeExclusionKeepsEligiblePin(t *testing.T) {
	store := newStubPinStore()
	store.getFound = true
	store.getPin = upgradeTestPin()
	fresh := upgradeTestFresh()
	store.getPin.DemotedModels = []string{fresh.Model}
	strategy := router.Strategy("upgrade-evidence-exclusion")
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, upgradeTestPinModel, nil).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: strategy,
			Router:   &authorityShadowTestRouter{decision: fresh},
			Capabilities: policy.Capabilities{
				AuthoritativePerTurnSelection: true,
			},
		})
	env, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"continue"}]}`, catalog.ModelIDClaudeOpus48.String())))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(router.WithStrategy(context.Background(), strategy), env, features, "test-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	assert.Equal(t, upgradeTestPinModel, res.Decision.Model)
	assert.True(t, res.StickyHit)
	assert.Equal(t, string(pinTierAuthoritativeExcludedPin), res.PinTier)
	require.Len(t, store.upserts, 1)
	assert.Equal(t, upgradeTestPinModel, store.upserts[0].Model)
}

func TestEvidenceUpgradeExclusionFailsOpenWhenOnlyDemotedModelIsAvailable(t *testing.T) {
	store := newStubPinStore()
	store.getFound = true
	store.getPin = upgradeTestPin()
	fresh := upgradeTestFresh()
	store.getPin.DemotedModels = []string{store.getPin.Model, fresh.Model}
	strategy := router.Strategy("upgrade-evidence-exclusion-last-resort")
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, upgradeTestPinModel, nil).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: strategy,
			Router:   &authorityShadowTestRouter{decision: fresh},
			Capabilities: policy.Capabilities{
				AuthoritativePerTurnSelection: true,
			},
		})
	env, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"continue"}]}`, catalog.ModelIDClaudeOpus48.String())))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(router.WithStrategy(context.Background(), strategy), env, features, "test-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	assert.Equal(t, fresh.Model, res.Decision.Model)
	assert.False(t, res.StickyHit)
	assert.Equal(t, string(pinTierAuthoritativeExcludedReroute), res.PinTier)
	require.Len(t, store.upserts, 1)
	assert.Equal(t, fresh.Model, store.upserts[0].Model)
}
