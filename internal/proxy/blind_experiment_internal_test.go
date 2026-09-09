package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/sessionpin"
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

func TestBlindExperimentPassthroughSkipsAutomaticPinsAndScorer(t *testing.T) {
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
	assert.True(t, loopResult.BlindExperimentPassthrough)
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

func TestBlindExperimentPassthroughLeavesAutomaticSessionHistoryUntouched(t *testing.T) {
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
	assert.True(t, loopResult.BlindExperimentPassthrough)
	pins.mu.Lock()
	defer pins.mu.Unlock()
	assert.Equal(t, []string{forceModelSessionRole}, pins.getRoles,
		"automatic, HMM, and force-model history rows must remain unread")
	assert.Empty(t, pins.upserts)
	assert.Zero(t, pins.usageHits)
}

func TestBlindExperimentPassthroughUsesGatewayAlias(t *testing.T) {
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

func TestBlindExperimentPassthroughHonorsExcludedModels(t *testing.T) {
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
	assert.False(t, loopResult.BlindExperimentPassthrough)
	assert.Equal(t, "claude-haiku-4-5", loopResult.Decision.Model)
}

func TestApplyBlindExperimentTelemetry(t *testing.T) {
	telemetry := InsertTelemetryParams{TrainingAllowed: true}
	applyBlindExperimentTelemetry(blindExperimentContext(auth.BlindExperimentArmPassthrough), &telemetry)

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
