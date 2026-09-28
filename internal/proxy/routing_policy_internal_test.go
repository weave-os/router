package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type routingPolicyStub struct {
	mode     auth.RoutingPolicyMode
	assigned bool
}

func (stub routingPolicyStub) GetPolicy(context.Context, string) (auth.RoutingPolicy, error) {
	return auth.RoutingPolicy{Mode: stub.mode, Revision: 1}, nil
}

func (stub routingPolicyStub) HasAssignment(context.Context, string, string, int64) (bool, error) {
	return stub.assigned, nil
}

func TestExplicitRoutingPolicyPrecedesClassifierAndPins(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     auth.RoutingPolicyMode
		assigned bool
	}{
		{name: "off", mode: auth.RoutingPolicyPassthrough},
		{name: "teams unselected", mode: auth.RoutingPolicyAssigned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routerSpy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
			pins := newStubPinStore()
			service := NewService(routerSpy, nil, nil, false, nil, pins, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
			authService := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(routingPolicyStub{mode: tc.mode, assigned: tc.assigned}, nil)
			ctx, err := authService.WithRoutingPolicy(context.Background(), "installation")
			require.NoError(t, err)
			ctx, err = authService.WithRoutingAssignment(ctx, "installation", "user")
			require.NoError(t, err)
			envelope, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`))
			require.NoError(t, err)
			request := router.Request{RequestedModel: catalog.ModelIDClaudeSonnet46.String(), EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}
			turn, err := service.runTurnLoop(ctx, envelope, envelope.RoutingFeatures(false), "api-key", uuid.New(), "", http.Header{}, request)
			require.NoError(t, err)
			assert.True(t, turn.CallerModelPassthrough)
			assert.Equal(t, catalog.ModelIDClaudeSonnet46.String(), turn.Decision.Model)
			assert.Empty(t, routingMarkerFor(turn))
			assert.Equal(t, 0, routerSpy.routeCalls)
			assert.False(t, turn.UsageBypass)
			pins.mu.Lock()
			defer pins.mu.Unlock()
			assert.Equal(t, []string{forceModelSessionRole}, pins.getRoles)
		})
	}
}

func TestRoutingPolicyPreservesExplicitForcedModel(t *testing.T) {
	for _, mode := range []auth.RoutingPolicyMode{auth.RoutingPolicyPassthrough, auth.RoutingPolicyAssigned} {
		for _, tc := range []struct {
			name        string
			requestOnly bool
			cleared     bool
			excluded    bool
		}{
			{name: "request override", requestOnly: true},
			{name: "saved session pin"},
			{name: "cleared session pin", cleared: true},
			{name: "excluded request override", requestOnly: true, excluded: true},
			{name: "excluded saved pin", excluded: true},
		} {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				pins := newStubPinStore()
				forceModel := catalog.ModelIDClaudeHaiku45.String()
				if !tc.requestOnly {
					forceModel = ""
					pins.getFound = true
					pins.getPin = sessionpin.Pin{
						Model: catalog.ModelIDClaudeHaiku45.String(), Provider: providers.ProviderAnthropic,
						Reason: translate.ReasonUserForceModel, PinnedUntil: pinNeverExpires,
					}
					if tc.cleared {
						pins.getPin.Reason = userUnforcedReason
					}
				}
				routerSpy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
				service := NewService(routerSpy, nil, nil, false, nil, pins, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
				authService := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(routingPolicyStub{mode: mode}, nil)
				ctx, err := authService.WithRoutingPolicy(context.Background(), "installation")
				require.NoError(t, err)
				ctx, err = authService.WithRoutingAssignment(ctx, "installation", "user")
				require.NoError(t, err)
				if tc.excluded {
					ctx = context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{catalog.ModelIDClaudeHaiku45.String()})
				}
				envelope, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`))
				require.NoError(t, err)
				turn, err := service.runTurnLoop(ctx, envelope, envelope.RoutingFeatures(false), "api-key", uuid.New(), "", http.Header{}, router.Request{
					RequestedModel: catalog.ModelIDClaudeSonnet46.String(), ForceModel: forceModel, EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
				})
				assert.Zero(t, routerSpy.routeCalls)
				if tc.excluded {
					require.ErrorIs(t, err, ErrForcedModelExcluded)
					return
				}
				require.NoError(t, err)
				if tc.cleared {
					assert.Equal(t, catalog.ModelIDClaudeSonnet46.String(), turn.Decision.Model)
					assert.True(t, turn.CallerModelPassthrough)
					return
				}
				assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), turn.Decision.Model)
				assert.Equal(t, translate.ReasonUserForceModel, turn.Decision.Reason)
				assert.False(t, turn.CallerModelPassthrough)
			})
		}
	}
}
