package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func blindExperimentContext(arm auth.BlindExperimentArm) context.Context {
	return context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
		Active:              true,
		Arm:                 arm,
		AssignmentSource:    auth.BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: "account-1",
	})
}

type blindExperimentRouterSpy struct {
	decision   router.Decision
	err        error
	routeCalls int
}

func (spy *blindExperimentRouterSpy) Route(context.Context, router.Request) (router.Decision, error) {
	spy.routeCalls++
	return spy.decision, spy.err
}

func TestCallerModelPassthroughSkipsAutomaticPinsAndScorer(t *testing.T) {
	routerSpy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
	pins := newStubPinStore()
	service := NewService(routerSpy, nil, nil, false, nil, pins, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	envelope, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)

	loopResult, err := service.runTurnLoop(
		blindExperimentContext(auth.BlindExperimentArmPassthrough),
		envelope,
		envelope.RoutingFeatures(false),
		"api-key",
		uuid.New(),
		"",
		http.Header{},
		router.Request{
			RequestedModel: "claude-sonnet-4-6",
			EnabledProviders: map[string]struct{}{
				providers.ProviderAnthropic: {},
			},
		},
	)

	require.NoError(t, err)
	assert.Equal(t, 0, routerSpy.routeCalls)
	assert.True(t, loopResult.CallerModelPassthrough)
	assert.False(t, loopResult.UsageBypass)
	assert.Equal(t, "claude-sonnet-4-6", loopResult.Decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, loopResult.Decision.Provider)
	assert.Equal(t, blindExperimentPublicDecisionReason, loopResult.Decision.Reason)
	pins.mu.Lock()
	defer pins.mu.Unlock()
	assert.Equal(t, []string{forceModelSessionRole}, pins.getRoles,
		"passthrough may inspect explicit force-model state but must not read automatic or history pins")
	assert.Empty(t, pins.upserts, "automatic pins must not be written before experiment passthrough")
	assert.Zero(t, pins.usageHits, "passthrough must not update automatic pin usage")
}

func TestCallerModelPassthroughLeavesAutomaticSessionHistoryUntouched(t *testing.T) {
	pins := newStubPinStore()
	service := NewService(nil, nil, nil, false, nil, pins, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	envelope, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)

	loopResult, err := service.runTurnLoop(
		blindExperimentContext(auth.BlindExperimentArmPassthrough),
		envelope,
		envelope.RoutingFeatures(false),
		"api-key",
		uuid.New(),
		"",
		http.Header{},
		router.Request{RequestedModel: "claude-sonnet-4-6"},
	)

	require.NoError(t, err)
	assert.Equal(t, [sessionpin.SessionKeyLen]byte{}, loopResult.SessionKey)
	assert.Empty(t, loopResult.PriorServedModel)
	assert.False(t, loopResult.SessionEverSwitched)
	assert.True(t, loopResult.CallerModelPassthrough)
	pins.mu.Lock()
	defer pins.mu.Unlock()
	assert.Equal(t, []string{forceModelSessionRole}, pins.getRoles,
		"automatic, HMM, and force-model history rows must remain unread")
	assert.Empty(t, pins.upserts)
	assert.Zero(t, pins.usageHits)
}

func TestCallerModelPassthroughUsesGatewayAlias(t *testing.T) {
	service := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	decision, passthrough, err := service.blindExperimentPassthroughDecision(
		blindExperimentContext(auth.BlindExperimentArmPassthrough),
		router.Request{
			RequestedModel: "gpt-5.4",
			EnabledProviders: map[string]struct{}{
				providers.ProviderOpenAIGateway:    {},
				providers.ProviderAnthropicGateway: {},
			},
			GatewayProviders: map[string]struct{}{
				providers.ProviderAnthropicGateway: {},
			},
			CustomBindings: map[string][]string{
				"gpt-5.4": {providers.ProviderAnthropicGateway},
			},
		},
	)

	require.NoError(t, err)
	assert.True(t, passthrough)
	assert.Equal(t, providers.ProviderAnthropicGateway, decision.Provider,
		"gateway-exclusive passthrough must use the held key's alias instead of the catalog gateway binding")
	assert.Equal(t, "gpt-5.4", decision.Model)
	assert.Equal(t, blindExperimentPublicDecisionReason, decision.Reason)
}

func TestCallerModelPassthroughRejectsUnknownModelWithoutBlamingProviderKeys(t *testing.T) {
	service := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	_, _, err := service.blindExperimentPassthroughDecision(
		blindExperimentContext(auth.BlindExperimentArmPassthrough),
		router.Request{
			RequestedModel:   "auto",
			EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
		},
	)

	var unknown *PassthroughModelUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, "auto", unknown.Model)
	assert.False(t, unknown.RoutingPolicyPassthrough, "the blind-experiment arm is not the org routing policy")
	assert.NotErrorIs(t, err, cluster.ErrNoEligibleProvider,
		"a routing placeholder must not surface as the missing-provider-keys error")
}

func TestCallerModelPassthroughResolvesDatedAlias(t *testing.T) {
	service := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	decision, _, err := service.blindExperimentPassthroughDecision(
		blindExperimentContext(auth.BlindExperimentArmPassthrough),
		router.Request{
			RequestedModel:   "claude-haiku-4-5-20251001",
			EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		},
	)

	require.NoError(t, err, "a dated catalog alias is a known model and must still pass through")
	assert.Equal(t, providers.ProviderAnthropic, decision.Provider)
	assert.Equal(t, "claude-haiku-4-5-20251001", decision.Model)
}

func TestCallerModelPassthroughHonorsExcludedModels(t *testing.T) {
	routerSpy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
	service := NewService(routerSpy, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	envelope, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)

	_, err = service.runTurnLoop(
		blindExperimentContext(auth.BlindExperimentArmPassthrough),
		envelope,
		envelope.RoutingFeatures(false),
		"api-key",
		uuid.New(),
		"",
		http.Header{},
		router.Request{
			RequestedModel: "claude-sonnet-4-6",
			ExcludedModels: map[string]struct{}{
				"claude-sonnet-4-6": {},
			},
		},
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, cluster.ErrNoEligibleProvider)
	assert.Zero(t, routerSpy.routeCalls, "an excluded requested model must fail directly instead of falling through to automatic routing")
}

func blindExperimentUtilityTurnBodies() []struct {
	turnType turntype.TurnType
	body     string
} {
	return []struct {
		turnType turntype.TurnType
		body     string
	}{
		{turntype.Probe, `{"model":"claude-opus-4-8","max_tokens":1,"messages":[{"role":"user","content":"quota"}]}`},
		{turntype.TitleGen, `{"model":"claude-opus-4-8","max_tokens":1024,"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}}},"messages":[{"role":"user","content":"title"}]}`},
		{turntype.Compaction, `{"model":"claude-opus-4-8","max_tokens":1024,"system":"Your task is to create a detailed summary","messages":[{"role":"user","content":"summary"}]}`},
		{turntype.SubAgentDispatch, `{"model":"claude-opus-4-8","max_tokens":1024,"metadata":{"user_id":"subagent:Explore"},"messages":[{"role":"user","content":"list go files"}]}`},
	}
}

func runBlindExperimentUtilityTurn(t *testing.T, ctx context.Context, body string) turnLoopResult {
	t.Helper()
	routerSpy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
	service := NewService(routerSpy, nil, nil, false, nil, newStubPinStore(), false,
		providers.ProviderGoogle, "gemini-3.1-flash-lite-preview", nil).
		WithExplicitUtilityHardPin(true).
		WithCompactionHardPin(true).
		WithSubAgentOverride(providers.ProviderGoogle, "gemini-3-flash-preview")
	envelope, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	features := envelope.RoutingFeatures(false)
	loopResult, err := service.runTurnLoop(
		ctx,
		envelope,
		features,
		"api-key",
		uuid.New(),
		"",
		http.Header{},
		router.Request{
			RequestedModel: features.Model,
			EnabledProviders: map[string]struct{}{
				providers.ProviderAnthropic: {},
				providers.ProviderGoogle:    {},
			},
		},
	)
	require.NoError(t, err)
	assert.Zero(t, routerSpy.routeCalls)
	return loopResult
}

func TestBlindExperimentPassthroughOutranksUtilityHardPins(t *testing.T) {
	for _, testCase := range blindExperimentUtilityTurnBodies() {
		t.Run(string(testCase.turnType), func(t *testing.T) {
			loopResult := runBlindExperimentUtilityTurn(t, blindExperimentContext(auth.BlindExperimentArmPassthrough), testCase.body)

			require.Equal(t, testCase.turnType, loopResult.TurnType)
			assert.True(t, loopResult.CallerModelPassthrough)
			assert.False(t, loopResult.HardPinned)
			assert.Empty(t, loopResult.Purpose, "a passthrough turn is not authorized as deployment utility work")
			assert.Equal(t, "claude-opus-4-8", loopResult.Decision.Model)
			assert.Equal(t, providers.ProviderAnthropic, loopResult.Decision.Provider)
			assert.Equal(t, blindExperimentPublicDecisionReason, loopResult.Decision.Reason)
		})
	}
}

func TestBlindExperimentRouterOnKeepsExplicitUtilityHardPins(t *testing.T) {
	for _, testCase := range blindExperimentUtilityTurnBodies() {
		t.Run(string(testCase.turnType), func(t *testing.T) {
			loopResult := runBlindExperimentUtilityTurn(t, blindExperimentContext(auth.BlindExperimentArmRouterOn), testCase.body)

			require.Equal(t, testCase.turnType, loopResult.TurnType)
			assert.True(t, loopResult.HardPinned)
			assert.False(t, loopResult.CallerModelPassthrough)
			assert.Equal(t, string(testCase.turnType)+"_hard_pin", loopResult.Decision.Reason)
			assert.NotEqual(t, "claude-opus-4-8", loopResult.Decision.Model)
		})
	}
}

func TestBlindExperimentPassthroughKeepsUtilityHardPinsUnderPolicyPin(t *testing.T) {
	ctx := router.WithPolicyPinRequest(blindExperimentContext(auth.BlindExperimentArmPassthrough), router.PolicyPinRequest{
		Pin: router.PolicyPin{
			ArtifactSHA256: strings.Repeat("a", 64),
			RosterSHA256:   strings.Repeat("b", 64),
		},
		Authorized: true,
	})
	for _, testCase := range blindExperimentUtilityTurnBodies() {
		t.Run(string(testCase.turnType), func(t *testing.T) {
			loopResult := runBlindExperimentUtilityTurn(t, ctx, testCase.body)

			require.Equal(t, testCase.turnType, loopResult.TurnType)
			assert.True(t, loopResult.HardPinned)
			assert.False(t, loopResult.CallerModelPassthrough)
			assert.Equal(t, string(testCase.turnType)+"_hard_pin", loopResult.Decision.Reason)
			assert.NotEqual(t, "claude-opus-4-8", loopResult.Decision.Model)
		})
	}
}

func TestBlindExperimentPassthroughYieldsUtilityTurnToForceModel(t *testing.T) {
	routerSpy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
	service := NewService(routerSpy, nil, nil, false, nil, newStubPinStore(), false,
		providers.ProviderGoogle, "gemini-3.1-flash-lite-preview", nil)
	envelope, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-8","max_tokens":1,"messages":[{"role":"user","content":"quota"}]}`))
	require.NoError(t, err)

	loopResult, err := service.runTurnLoop(
		blindExperimentContext(auth.BlindExperimentArmPassthrough),
		envelope,
		envelope.RoutingFeatures(false),
		"api-key",
		uuid.New(),
		"",
		http.Header{},
		router.Request{
			RequestedModel:   "claude-opus-4-8",
			ForceModel:       "claude-sonnet-4-6",
			EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		},
	)

	require.NoError(t, err)
	require.Equal(t, turntype.Probe, loopResult.TurnType)
	assert.False(t, loopResult.CallerModelPassthrough)
	assert.Equal(t, "claude-sonnet-4-6", loopResult.Decision.Model)
	assert.Equal(t, translate.ReasonUserForceModel, loopResult.Decision.Reason)
}

func TestBlindExperimentRouterOnUsesScorer(t *testing.T) {
	routerSpy := &blindExperimentRouterSpy{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Reason:   "cluster:test",
	}}
	service := NewService(routerSpy, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	envelope, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)

	loopResult, err := service.runTurnLoop(
		blindExperimentContext(auth.BlindExperimentArmRouterOn),
		envelope,
		envelope.RoutingFeatures(false),
		"api-key",
		uuid.New(),
		"",
		http.Header{},
		router.Request{RequestedModel: "claude-sonnet-4-6"},
	)

	require.NoError(t, err)
	assert.Equal(t, 1, routerSpy.routeCalls)
	assert.False(t, loopResult.CallerModelPassthrough)
	assert.Equal(t, "claude-haiku-4-5", loopResult.Decision.Model)
}

func TestApplyBlindExperimentTelemetry(t *testing.T) {
	telemetry := InsertTelemetryParams{TrainingAllowed: true}
	applyBlindExperimentTelemetry(blindExperimentContext(auth.BlindExperimentArmPassthrough), &telemetry, nil)

	assert.Equal(t, auth.BlindExperimentArmPassthrough, telemetry.BlindExperimentArm)
	assert.Equal(t, auth.BlindExperimentAssignmentAutomatic, telemetry.BlindExperimentAssignmentSource)
	assert.Equal(t, "account-1", telemetry.BlindExperimentSubjectKey)
	assert.False(t, telemetry.TrainingAllowed)

	observation := buildObservationContext(
		context.WithValue(blindExperimentContext(auth.BlindExperimentArmPassthrough), PolicyTrainingAllowedContextKey{}, true),
		router.Decision{},
		router.Decision{},
		CaptureOff,
	)
	assert.False(t, observation.TrainingAllowed)
}

func TestCohortTelemetrySeparatesScheduledIntendedAndApplied(t *testing.T) {
	state := auth.BlindExperimentState{Active: true, Enabled: true, Arm: auth.BlindExperimentArmPassthrough,
		ScheduledArm: auth.BlindExperimentArmRouterOn, CohortExperimentID: uuid.NewString(),
		CohortGroupID: 3, CohortPhaseIndex: 2, CohortRevision: 4,
		AssignmentSource: auth.BlindExperimentAssignmentManual, CanonicalSubjectKey: "account-1"}
	ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, state)
	params := InsertTelemetryParams{}
	applyBlindExperimentTelemetry(ctx, &params, &turnLoopResult{
		Decision: router.Decision{Model: "claude-sonnet-4-6"}, CallerModelPassthrough: true})
	assert.Equal(t, auth.BlindExperimentArmRouterOn, params.CohortScheduledArm)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, params.BlindExperimentArm)
	require.NotNil(t, params.CohortTreatmentApplied)
	assert.True(t, *params.CohortTreatmentApplied)

	state.Arm = auth.BlindExperimentArmRouterOn
	ctx = context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, state)
	params = InsertTelemetryParams{}
	applyBlindExperimentTelemetry(ctx, &params, &turnLoopResult{Decision: router.Decision{Model: "claude-sonnet-4-6"}, HardPinned: true})
	require.NotNil(t, params.CohortTreatmentApplied)
	assert.False(t, *params.CohortTreatmentApplied)
	assert.Equal(t, auth.CohortBypassHardPin, params.CohortBypassReason)
}

func TestCohortTelemetryRecordsUnassignedIdentityWithoutRoutingTreatment(t *testing.T) {
	state := auth.BlindExperimentState{Enabled: true, CohortExperimentID: uuid.NewString()}
	ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, state)
	params := InsertTelemetryParams{}
	applyBlindExperimentTelemetry(ctx, &params, nil)
	assert.Equal(t, state.CohortExperimentID, params.CohortExperimentID)
	assert.Nil(t, params.CohortTreatmentApplied)
	assert.Equal(t, auth.CohortBypassUnassigned, params.CohortBypassReason)
}

func TestCohortTelemetryExplainsBypassedTreatment(t *testing.T) {
	state := auth.BlindExperimentState{Active: true, Enabled: true, Arm: auth.BlindExperimentArmRouterOn,
		ScheduledArm: auth.BlindExperimentArmRouterOn, CohortExperimentID: uuid.NewString(),
		CohortGroupID: 2, CohortPhaseIndex: 1, CohortRevision: 1}
	ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, state)
	for _, testCase := range []struct {
		name   string
		routed *turnLoopResult
		reason auth.CohortBypassReason
	}{
		{name: "force model", routed: &turnLoopResult{Decision: router.Decision{Model: "model", Reason: translate.ReasonUserForceModel}}, reason: auth.CohortBypassForceModel},
		{name: "hard pin", routed: &turnLoopResult{HardPinned: true, Decision: router.Decision{Model: "model"}}, reason: auth.CohortBypassHardPin},
		{name: "usage bypass", routed: &turnLoopResult{UsageBypass: true, Decision: router.Decision{Model: "model"}}, reason: auth.CohortBypassUsageBypass},
		{name: "not dispatched", routed: nil, reason: auth.CohortBypassNotDispatched},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			params := InsertTelemetryParams{}
			applyBlindExperimentTelemetry(ctx, &params, testCase.routed)
			require.NotNil(t, params.CohortTreatmentApplied)
			assert.False(t, *params.CohortTreatmentApplied)
			assert.Equal(t, testCase.reason, params.CohortBypassReason)
		})
	}
}
