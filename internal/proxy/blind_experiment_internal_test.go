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
	"weave-os/router/internal/router/catalog"
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

func TestCallerModelPassthroughServesWeaveAutoOnTheClientDefault(t *testing.T) {
	service := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	enabled := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}}
	for clientApp, want := range map[string]string{ClientAppClaudeCode: "claude-opus-5-5", ClientAppCodex: "gpt-6.1-sol"} {
		ctx := context.WithValue(blindExperimentContext(auth.BlindExperimentArmPassthrough),
			ClientIdentityContextKey{}, ClientIdentity{ClientApp: clientApp})

		decision, _, err := service.blindExperimentPassthroughDecision(ctx,
			router.Request{RequestedModel: "weave-auto", EnabledProviders: enabled})

		require.NoError(t, err, "a control-arm %s user on Weave Auto must still be served", clientApp)
		assert.Equal(t, want, decision.Model)
	}
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
		providers.ProviderGoogle, experimentUtilityHardPinModel, nil).
		WithExplicitUtilityHardPin(true).
		WithCompactionHardPin(true).
		WithSubAgentOverride(providers.ProviderGoogle, experimentSubAgentOverrideModel)
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
			switch testCase.turnType {
			case turntype.Probe, turntype.TitleGen:
				assert.Equal(t, experimentUtilityHardPinModel, loopResult.Decision.Model)
			case turntype.SubAgentDispatch:
				assert.Equal(t, experimentSubAgentOverrideModel, loopResult.Decision.Model)
			default:
				assert.NotEqual(t, catalog.ModelIDClaudeOpus48.String(), loopResult.Decision.Model)
			}
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
			switch testCase.turnType {
			case turntype.Probe, turntype.TitleGen:
				assert.Equal(t, experimentUtilityHardPinModel, loopResult.Decision.Model)
			case turntype.SubAgentDispatch:
				assert.Equal(t, experimentSubAgentOverrideModel, loopResult.Decision.Model)
			default:
				assert.NotEqual(t, catalog.ModelIDClaudeOpus48.String(), loopResult.Decision.Model)
			}
		})
	}
}

const experimentUtilityHardPinModel = "gemini-3.1-flash-lite-preview"
const experimentSubAgentOverrideModel = "gemini-3-flash-preview"

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
	assert.Empty(t, telemetry.BlindExperimentSessionKey)
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

func TestApplyBlindExperimentTelemetryRecordsSessionDraw(t *testing.T) {
	ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
		Active: true, Arm: auth.BlindExperimentArmRouterOn, AssignmentSource: auth.BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: "account-1", Mode: auth.BlindExperimentModeSession, SessionSubjectKey: "session:abc",
	})
	telemetry := InsertTelemetryParams{}
	applyBlindExperimentTelemetry(ctx, &telemetry, nil)
	assert.Equal(t, auth.BlindExperimentModeSession, telemetry.BlindExperimentMode)
	assert.Equal(t, "session:abc", telemetry.BlindExperimentSessionKey)
}

func TestApplyBlindExperimentTelemetryLabelsSessionModeWithoutSessionAsUser(t *testing.T) {
	ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
		Active: true, Arm: auth.BlindExperimentArmRouterOn, AssignmentSource: auth.BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: "account-1", Mode: auth.BlindExperimentModeSession,
	})
	telemetry := InsertTelemetryParams{}
	applyBlindExperimentTelemetry(ctx, &telemetry, nil)
	assert.Equal(t, auth.BlindExperimentModeUser, telemetry.BlindExperimentMode)
	assert.Empty(t, telemetry.BlindExperimentSessionKey)
}

func TestBlindPassthroughUnknownModelServesClientBaseline(t *testing.T) {
	for _, tc := range []struct {
		name      string
		client    string
		requested string
		provider  string
		want      string
	}{
		{name: "claude code retired id", client: ClientAppClaudeCode, requested: "claude-retired-9", provider: providers.ProviderAnthropic, want: "claude-opus-5-5"},
		{name: "codex retired id", client: ClientAppCodex, requested: "gpt-retired-1", provider: providers.ProviderOpenAI, want: "gpt-6.1-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := NewService(nil, nil, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
			ctx := context.WithValue(blindExperimentContext(auth.BlindExperimentArmPassthrough), ClientIdentityContextKey{}, ClientIdentity{ClientApp: tc.client})

			decision, handled, err := service.blindExperimentPassthroughDecision(ctx, router.Request{
				RequestedModel:   tc.requested,
				EnabledProviders: map[string]struct{}{tc.provider: {}},
			})

			require.NoError(t, err, "router_on serves an unknown id, so the passthrough arm must too")
			assert.True(t, handled)
			assert.Equal(t, tc.want, decision.Model)
			assert.Equal(t, blindExperimentPublicDecisionReason, decision.Reason)
		})
	}
}

func TestBlindPassthroughUnknownModelBaselineHonoursExclusions(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  router.Request
	}{
		{name: "excluded", req: router.Request{ExcludedModels: map[string]struct{}{"claude-opus-5-5": {}}}},
		{name: "safety excluded", req: router.Request{SafetyExcludedModels: map[string]struct{}{"claude-opus-5-5": {}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := NewService(nil, nil, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
			ctx := context.WithValue(blindExperimentContext(auth.BlindExperimentArmPassthrough), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppClaudeCode})
			req := tc.req
			req.RequestedModel = "claude-retired-9"
			req.EnabledProviders = map[string]struct{}{providers.ProviderAnthropic: {}}

			decision, _, err := service.blindExperimentPassthroughDecision(ctx, req)

			require.Error(t, err, "the swapped-in baseline must not bypass the installation's model restrictions")
			assert.Empty(t, decision.Model)
			var unknown *PassthroughModelUnknownError
			assert.False(t, errors.As(err, &unknown), "the error must be the one router_on gives for an ineligible model")
			assert.True(t, errors.Is(err, cluster.ErrNoEligibleProvider) || errors.Is(err, cluster.ErrAllowlistEmptiesPool))
		})
	}
}

func TestBlindExperimentTelemetryNamesWhatDecidedTheModel(t *testing.T) {
	decision := router.Decision{Model: "claude-sonnet-4-6"}
	for _, tc := range []struct {
		name        string
		arm         auth.BlindExperimentArm
		client      string
		cohort      bool
		clientModel bool
		policy      bool
		requested   string
		served      string
		routed      *turnLoopResult
		source      auth.BlindExperimentModelSource
		applied     bool
		baseline    string
		changed     bool
	}{
		{name: "router_on routed", arm: auth.BlindExperimentArmRouterOn, requested: "weave-auto", routed: &turnLoopResult{Decision: decision}, source: auth.BlindExperimentModelSourceRouter, applied: true, baseline: "claude-opus-5-5", changed: true},
		{name: "router_on routed keeps requested", arm: auth.BlindExperimentArmRouterOn, requested: "claude-sonnet-4-6", routed: &turnLoopResult{Decision: decision}, source: auth.BlindExperimentModelSourceRouter, applied: true, baseline: "claude-sonnet-4-6"},
		{name: "router_on retry served another model", arm: auth.BlindExperimentArmRouterOn, requested: "claude-sonnet-4-6", served: "claude-opus-5-5", routed: &turnLoopResult{Decision: decision}, source: auth.BlindExperimentModelSourceRouter, applied: true, baseline: "claude-sonnet-4-6", changed: true},
		{name: "router_on named client pick", arm: auth.BlindExperimentArmRouterOn, clientModel: true, requested: "claude-sonnet-4-6", routed: &turnLoopResult{Decision: decision, CallerModelPassthrough: true}, source: auth.BlindExperimentModelSourceClientModelPassthrough, baseline: "claude-sonnet-4-6"},
		{name: "passthrough arm", arm: auth.BlindExperimentArmPassthrough, requested: "weave-auto", routed: &turnLoopResult{Decision: router.Decision{Model: "claude-opus-5-5"}, CallerModelPassthrough: true}, source: auth.BlindExperimentModelSourceExperimentPassthrough, applied: true, baseline: "claude-opus-5-5"},
		{name: "passthrough arm retired id", arm: auth.BlindExperimentArmPassthrough, requested: "claude-retired-9", routed: &turnLoopResult{Decision: router.Decision{Model: "claude-opus-5-5"}, CallerModelPassthrough: true}, source: auth.BlindExperimentModelSourceExperimentPassthrough, applied: true, baseline: "claude-opus-5-5"},
		{name: "passthrough arm named client pick", arm: auth.BlindExperimentArmPassthrough, clientModel: true, requested: "claude-sonnet-4-6", routed: &turnLoopResult{Decision: decision, CallerModelPassthrough: true}, source: auth.BlindExperimentModelSourceClientModelPassthrough, baseline: "claude-sonnet-4-6"},
		{name: "routing policy passthrough", arm: auth.BlindExperimentArmRouterOn, policy: true, requested: "claude-sonnet-4-6", routed: &turnLoopResult{Decision: decision, CallerModelPassthrough: true}, source: auth.BlindExperimentModelSourceRoutingPolicyPassthrough, baseline: "claude-sonnet-4-6"},
		{name: "hard pin", arm: auth.BlindExperimentArmPassthrough, cohort: true, requested: "claude-opus-5-5", routed: &turnLoopResult{Decision: decision, HardPinned: true}, source: auth.BlindExperimentModelSourceHardPin, baseline: "claude-opus-5-5", changed: true},
		{name: "force model", arm: auth.BlindExperimentArmRouterOn, requested: "weave-auto", routed: &turnLoopResult{Decision: router.Decision{Model: "claude-sonnet-4-6", Reason: translate.ReasonUserForceModel}}, source: auth.BlindExperimentModelSourceForceModel, baseline: "claude-opus-5-5", changed: true},
		{name: "usage bypass", arm: auth.BlindExperimentArmRouterOn, requested: "claude-sonnet-4-6", routed: &turnLoopResult{Decision: decision, UsageBypass: true}, source: auth.BlindExperimentModelSourceUsageBypass, baseline: "claude-sonnet-4-6"},
		{name: "not dispatched", arm: auth.BlindExperimentArmRouterOn, requested: "weave-auto", source: auth.BlindExperimentModelSourceNotDispatched, baseline: "claude-opus-5-5"},
		{name: "alias from a client without a default", arm: auth.BlindExperimentArmRouterOn, client: "unknown-client", requested: "weave-auto", routed: &turnLoopResult{Decision: decision}, source: auth.BlindExperimentModelSourceRouter, applied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := auth.BlindExperimentState{Active: true, Enabled: true, Arm: tc.arm,
				AssignmentSource: auth.BlindExperimentAssignmentAutomatic, CanonicalSubjectKey: "account-1"}
			if tc.cohort {
				state.CohortExperimentID, state.CohortGroupID, state.ScheduledArm = uuid.NewString(), 1, tc.arm
			}
			client := ClientAppClaudeCode
			if tc.client != "" {
				client = tc.client
			}
			ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, state)
			ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: client})
			if tc.clientModel {
				ctx = auth.WithClientModelPassthrough(ctx)
			}
			if tc.policy {
				ctx = auth.WithRoutingPolicy(ctx, auth.RoutingPolicy{Mode: auth.RoutingPolicyPassthrough})
			}
			params := InsertTelemetryParams{RequestedModel: tc.requested, DecisionModel: tc.served}
			if tc.routed != nil && tc.served == "" {
				params.DecisionModel = tc.routed.Decision.Model
			}

			applyBlindExperimentTelemetry(ctx, &params, tc.routed)

			assert.Equal(t, tc.arm, params.BlindExperimentArm)
			assert.Equal(t, tc.source, params.BlindExperimentModelSource)
			require.NotNil(t, params.BlindExperimentTreatmentApplied)
			assert.Equal(t, tc.applied, *params.BlindExperimentTreatmentApplied)
			assert.Equal(t, tc.baseline, params.BlindExperimentBaselineModel)
			if tc.routed == nil || tc.baseline == "" {
				assert.Nil(t, params.BlindExperimentModelChanged)
				return
			}
			require.NotNil(t, params.BlindExperimentModelChanged)
			assert.Equal(t, tc.changed, *params.BlindExperimentModelChanged)
		})
	}
}
