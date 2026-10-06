package proxy

import (
	"context"
	"net/http"
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
						assert.False(t, turn.CallerModelPassthrough)
					} else {
						assert.Zero(t, scorer.routeCalls)
						assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), turn.Decision.Model)
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
