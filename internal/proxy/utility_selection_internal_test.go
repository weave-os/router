package proxy

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func TestAutomaticUtilitySelectionDoesNotUseDeploymentShortcut(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"normal routing":       context.Background(),
		"experiment router on": blindExperimentContext(auth.BlindExperimentArmRouterOn),
		"honoured policy pin":  pinnedContext(true),
	} {
		t.Run(name, func(t *testing.T) {
			for _, fixture := range blindExperimentUtilityTurnBodies() {
				if fixture.turnType != turntype.TitleGen && fixture.turnType != turntype.Probe {
					continue
				}
				t.Run(string(fixture.turnType), func(t *testing.T) {
					scorer := &blindExperimentRouterSpy{decision: router.Decision{
						Provider: providers.ProviderAnthropic,
						Model:    catalog.ModelIDClaudeSonnet46.String(),
						Metadata: &router.RoutingMetadata{PolicyPinHonoured: true},
					}}
					pins := newStubPinStore()
					service := NewService(scorer, nil, nil, false, nil, pins, false,
						providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
					envelope, err := translate.ParseAnthropic([]byte(fixture.body))
					require.NoError(t, err)
					features := envelope.RoutingFeatures(false)
					turn, err := service.runTurnLoop(ctx, envelope, features, "utility-key", uuid.New(), "", http.Header{}, router.Request{
						RequestedModel:   features.Model,
						EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
					})
					require.NoError(t, err)
					assert.False(t, turn.HardPinned)
					assert.Zero(t, turn.SessionKey)
					assert.Empty(t, routingMarkerFor(turn))
					if fixture.turnType == turntype.TitleGen {
						assert.Equal(t, 1, scorer.routeCalls)
						assert.Equal(t, catalog.ModelIDClaudeSonnet46.String(), turn.Decision.Model)
						assert.Equal(t, providers.ProviderAnthropic, turn.Decision.Provider)
						assert.False(t, turn.CallerModelPassthrough)
					} else {
						assert.Zero(t, scorer.routeCalls)
						assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), turn.Decision.Model)
						assert.Equal(t, providers.ProviderAnthropic, turn.Decision.Provider)
						assert.True(t, turn.CallerModelPassthrough)
						assert.Equal(t, policy.OverrideSourceRequest, turn.Origin)
					}
					service.recordTurnUsage(ctx, turn, turn.Decision.Provider, turn.Decision.Model, 100, 10, 0, 0, false)
					pins.mu.Lock()
					defer pins.mu.Unlock()
					if fixture.turnType == turntype.TitleGen {
						assert.Equal(t, []string{forceModelSessionRole}, pins.getRoles)
					} else {
						assert.Equal(t, []string{forceModelSessionRole, roleForTier(catalog.TierFor(features.Model))}, pins.getRoles,
							"probes also inspect the legacy thread pin for explicit force-model compatibility")
					}
					assert.Empty(t, pins.upserts)
					assert.Zero(t, pins.usageHits)
				})
			}
		})
	}
}

func TestTitleNeverEmitsDroppedForceDiagnostic(t *testing.T) {
	assert.Empty(t, routingMarkerFor(turnLoopResult{
		TurnType:         turntype.TitleGen,
		Decision:         router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeSonnet46.String()},
		ForcedPinDropped: true,
		ForcedPinModel:   catalog.ModelIDClaudeOpus48.String(),
	}))
}

func TestBaselineMarkerNeverPrefixesMachineConsumedTurns(t *testing.T) {
	for _, turnType := range []turntype.TurnType{turntype.TitleGen, turntype.Probe, turntype.Classifier} {
		t.Run(string(turnType), func(t *testing.T) {
			assert.Empty(t, baselineRoutingMarkerFor(turnLoopResult{
				TurnType:         turnType,
				PriorServedModel: catalog.ModelIDGPT55.String(),
			}, catalog.ModelIDClaudeSonnet46.String()))
		})
	}
}

func TestAutomaticProbeWithoutConcreteTargetScoresWithoutPinning(t *testing.T) {
	for _, requestedModel := range []string{automaticProbeModel, ""} {
		t.Run(requestedModel, func(t *testing.T) {
			scorer := &blindExperimentRouterSpy{decision: router.Decision{
				Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeSonnet46.String(),
			}}
			pins := newStubPinStore()
			service := NewService(scorer, nil, nil, false, nil, pins, false,
				providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
			body := strings.Replace(blindExperimentUtilityTurnBodies()[0].body, catalog.ModelIDClaudeOpus48.String(), requestedModel, 1)
			envelope, err := translate.ParseAnthropic([]byte(body))
			require.NoError(t, err)
			features := envelope.RoutingFeatures(false)
			turn, err := service.runTurnLoop(context.Background(), envelope, features, "utility-key", uuid.New(), "", http.Header{}, router.Request{
				RequestedModel: features.Model, EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
			})
			require.NoError(t, err)
			require.Equal(t, turntype.Probe, turn.TurnType)
			assert.Equal(t, catalog.ModelIDClaudeSonnet46.String(), turn.Decision.Model)
			assert.Equal(t, providers.ProviderAnthropic, turn.Decision.Provider)
			assert.Equal(t, 1, scorer.routeCalls)
			assert.False(t, turn.CallerModelPassthrough)
			assert.False(t, turn.HardPinned)
			assert.Zero(t, turn.SessionKey)
			assert.Empty(t, turn.Purpose)
			assert.Empty(t, routingMarkerFor(turn))
			assert.Empty(t, pins.upserts)
		})
	}
}

func TestDefaultProbeDroppedForceDoesNotAttachConversationState(t *testing.T) {
	for _, requestedModel := range []string{automaticProbeModel, catalog.ModelIDClaudeSonnet46.String()} {
		t.Run(requestedModel, func(t *testing.T) {
			scorer := &blindExperimentRouterSpy{decision: router.Decision{
				Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeSonnet46.String(),
			}}
			pins := newStubPinStore()
			service := NewService(scorer, nil, nil, false, nil, pins, false,
				providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
			body := strings.Replace(blindExperimentUtilityTurnBodies()[0].body, catalog.ModelIDClaudeOpus48.String(), requestedModel, 1)
			envelope, err := translate.ParseAnthropic([]byte(body))
			require.NoError(t, err)
			features := envelope.RoutingFeatures(false)
			turn, err := service.runTurnLoop(context.Background(), envelope, features, "utility-key", uuid.New(), "", http.Header{}, router.Request{
				RequestedModel:   features.Model,
				ForceModel:       catalog.ModelIDGPT55.String(),
				EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
			})
			require.NoError(t, err)
			require.True(t, turn.ForcedPinDropped)
			assert.Empty(t, routingMarkerFor(turn), "probe output remains machine-readable when a force pin is unavailable")
			assert.Zero(t, turn.SessionKey)
			service.recordTurnUsage(context.Background(), turn, turn.Decision.Provider, turn.Decision.Model, 10, 1, 0, 0, false)
			pins.mu.Lock()
			defer pins.mu.Unlock()
			assert.Empty(t, pins.upserts)
			assert.Zero(t, pins.usageHits)
		})
	}
}
