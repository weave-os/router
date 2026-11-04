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
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/subscriptions"
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

func TestRoutingPolicyPassthroughUnknownModelIsAttributedToPolicy(t *testing.T) {
	service := NewService(nil, nil, nil, false, nil, nil, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
	authService := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(routingPolicyStub{mode: auth.RoutingPolicyPassthrough}, nil)
	ctx, err := authService.WithRoutingPolicy(context.Background(), "installation")
	require.NoError(t, err)

	_, err = service.callerModelPassthroughDecision(ctx, router.Request{
		RequestedModel:   "auto",
		EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
	})

	var unknown *PassthroughModelUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.True(t, unknown.RoutingPolicyPassthrough, "org routing off is the remedy the admin hint points at")
}

func TestCallerRoutingStateFollowsRoutingPrecedence(t *testing.T) {
	experiment := func(ctx context.Context, state auth.BlindExperimentState) context.Context {
		return context.WithValue(ctx, auth.BlindExperimentContextKey{}, state)
	}
	withPolicy := func(t *testing.T, mode auth.RoutingPolicyMode, assigned bool) context.Context {
		authService := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(routingPolicyStub{mode: mode, assigned: assigned}, nil)
		ctx, err := authService.WithRoutingPolicy(context.Background(), "installation")
		require.NoError(t, err)
		ctx, err = authService.WithRoutingAssignment(ctx, "installation", "user")
		require.NoError(t, err)
		return ctx
	}
	passthroughArm := auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmPassthrough}
	for _, tc := range []struct {
		name            string
		ctx             func(t *testing.T) context.Context
		wantPassthrough bool
		wantSource      callerRoutingSource
	}{
		{name: "no policy or experiment", ctx: func(*testing.T) context.Context { return context.Background() }, wantSource: callerRoutingSourceDefault},
		{name: "inactive experiment", ctx: func(*testing.T) context.Context {
			return experiment(context.Background(), auth.BlindExperimentState{Arm: auth.BlindExperimentArmPassthrough})
		}, wantSource: callerRoutingSourceDefault},
		{name: "experiment passthrough arm", ctx: func(*testing.T) context.Context { return experiment(context.Background(), passthroughArm) }, wantPassthrough: true, wantSource: callerRoutingSourceExperiment},
		{name: "experiment router on arm", ctx: func(*testing.T) context.Context {
			return experiment(context.Background(), auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn})
		}, wantSource: callerRoutingSourceExperiment},
		{name: "cohort passthrough phase", ctx: func(*testing.T) context.Context {
			return experiment(context.Background(), auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmPassthrough, CohortExperimentID: "cohort"})
		}, wantPassthrough: true, wantSource: callerRoutingSourceCohort},
		{name: "teams policy unassigned", ctx: func(t *testing.T) context.Context { return withPolicy(t, auth.RoutingPolicyAssigned, false) }, wantPassthrough: true, wantSource: callerRoutingSourceRoutingPolicy},
		{name: "teams policy assigned overrides experiment passthrough", ctx: func(t *testing.T) context.Context {
			return experiment(withPolicy(t, auth.RoutingPolicyAssigned, true), passthroughArm)
		}, wantSource: callerRoutingSourceRoutingPolicy},
		{name: "passthrough policy suppresses experiment router on arm", ctx: func(t *testing.T) context.Context {
			return experiment(withPolicy(t, auth.RoutingPolicyPassthrough, false), auth.BlindExperimentState{Active: true, Arm: auth.BlindExperimentArmRouterOn})
		}, wantPassthrough: true, wantSource: callerRoutingSourceRoutingPolicy},
		{name: "client-selected model overrides teams assignment", ctx: func(t *testing.T) context.Context {
			return auth.WithClientModelPassthrough(withPolicy(t, auth.RoutingPolicyAssigned, true))
		}, wantPassthrough: true, wantSource: callerRoutingSourceClientModel},
		{name: "inherit policy defers to experiment", ctx: func(t *testing.T) context.Context {
			return experiment(withPolicy(t, auth.RoutingPolicyInherit, false), passthroughArm)
		}, wantPassthrough: true, wantSource: callerRoutingSourceExperiment},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passthrough, source := callerRoutingState(tc.ctx(t))
			assert.Equal(t, tc.wantPassthrough, passthrough)
			assert.Equal(t, tc.wantSource, source)
		})
	}
}

func TestClientSelectedModelBypassesRoutingAndPins(t *testing.T) {
	routerSpy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
	service := NewService(routerSpy, nil, nil, false, nil, newStubPinStore(), false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
	authService := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(routingPolicyStub{mode: auth.RoutingPolicyAssigned, assigned: true}, nil)
	ctx, err := authService.WithRoutingPolicy(context.Background(), "installation")
	require.NoError(t, err)
	ctx, err = authService.WithRoutingAssignment(ctx, "installation", "user")
	require.NoError(t, err)
	require.False(t, auth.RoutingPassthroughFrom(ctx), "the assigned user routes unless the client selects a model")
	ctx = auth.WithClientModelPassthrough(ctx)
	envelope, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	request := router.Request{RequestedModel: catalog.ModelIDClaudeSonnet46.String(), EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}

	turn, err := service.runTurnLoop(ctx, envelope, envelope.RoutingFeatures(false), "api-key", uuid.New(), "", http.Header{}, request)

	require.NoError(t, err)
	assert.True(t, turn.CallerModelPassthrough)
	assert.Equal(t, catalog.ModelIDClaudeSonnet46.String(), turn.Decision.Model)
	assert.Equal(t, 0, routerSpy.routeCalls)
}

func TestClientSelectedUnknownModelIsNotAttributedToPolicy(t *testing.T) {
	service := NewService(nil, nil, nil, false, nil, nil, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)

	_, err := service.callerModelPassthroughDecision(auth.WithClientModelPassthrough(context.Background()), router.Request{
		RequestedModel:   "auto",
		EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
	})

	var unknown *PassthroughModelUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.False(t, unknown.RoutingPolicyPassthrough, "the client chose the model; no admin setting would fix it")
}

func TestPickedModelFundsOnCallersOwnPlan(t *testing.T) {
	picked := func() context.Context {
		return auth.WithClientModelPassthrough(context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, exhaustedSubToken))
	}
	creditFallback := func(ctx context.Context) context.Context {
		return context.WithValue(ctx, InstallationPickedModelCreditFallbackContextKey{}, true)
	}
	for _, tc := range []struct {
		name       string
		ctx        context.Context
		servedAsIs bool
		provider   string
		wantOwn    bool
		wantPicked bool
	}{
		{name: "picked model stays on the caller's login", ctx: picked(), servedAsIs: true, provider: providers.ProviderAnthropic, wantOwn: true, wantPicked: true},
		{name: "credit fallback opt-in releases it", ctx: creditFallback(picked()), servedAsIs: true, provider: providers.ProviderAnthropic, wantPicked: true},
		{name: "no login for the served provider funds like routed traffic", ctx: picked(), servedAsIs: true, provider: providers.ProviderOpenAI, wantPicked: true},
		{name: "a pick the org routed instead is routed traffic", ctx: picked(), provider: providers.ProviderAnthropic},
		{name: "routed request is untouched", ctx: context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, exhaustedSubToken), servedAsIs: true, provider: providers.ProviderAnthropic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := withPickedModelFunding(tc.ctx, tc.servedAsIs, tc.provider)

			assert.Equal(t, tc.wantOwn, pickedModelOnOwnPlan(ctx))
			assert.Equal(t, tc.wantOwn, paidFallbackForbidden(ctx))
			assert.Equal(t, tc.wantPicked, auth.ClientModelPassthroughFrom(ctx))
		})
	}
}

func TestPickedModelKeepsSpentPlanAndRefusesMeteredRescue(t *testing.T) {
	svc := &Service{
		usageObserver:            observerWithSnapshot(exhaustedSubToken, exhaustedSnapshot()),
		deploymentKeyedProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		now:                      func() time.Time { return time.Unix(1_000_000, 0) },
	}
	routed := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, exhaustedSubToken)
	picked := withPickedModelFunding(auth.WithClientModelPassthrough(routed), true, providers.ProviderAnthropic)
	model := catalog.ModelIDClaudeSonnet46.String()

	assert.True(t, svc.claudeSubscriptionExhausted(routed, http.Header{}), "routed traffic leaves a spent plan")
	assert.False(t, svc.claudeSubscriptionExhausted(picked, http.Header{}), "a pick stays on its own plan and lets the provider decide")
	assert.NoError(t, svc.subscriptionAPIAuthorization(routed, providers.ProviderAnthropic, model))
	assert.ErrorIs(t, svc.subscriptionAPIAuthorization(picked, providers.ProviderAnthropic, model), ErrSubscriptionPoolUnavailable)
}

func TestPickedCodexModelMaySpendOwnCredits(t *testing.T) {
	ctx := auth.WithClientModelPassthrough(context.WithValue(context.Background(), OpenAISubscriptionContextKey{}, "codex-jwt"))

	assert.True(t, requestcontext.SubscriptionExtraUsageFrom(withPickedModelFunding(ctx, true, providers.ProviderOpenAI)))
	assert.False(t, requestcontext.SubscriptionExtraUsageFrom(withPickedModelFunding(ctx, false, providers.ProviderOpenAI)))
}

func TestCodexPickFundsOnOwnPlan(t *testing.T) {
	selected := withCodexSelectedModel(context.WithValue(context.Background(), OpenAISubscriptionContextKey{}, "codex-jwt"), "gpt-6-astra", []byte(`{"reasoning":{"effort":"high"}}`))
	creditFallback := context.WithValue(selected, InstallationPickedModelCreditFallbackContextKey{}, true)

	served := withCodexPickFunding(selected, "gpt-6-astra", providers.ProviderOpenAI)
	assert.True(t, pickedModelOnOwnPlan(served))
	assert.True(t, requestcontext.SubscriptionExtraUsageFrom(served))
	assert.False(t, pickedModelOnOwnPlan(withCodexPickFunding(selected, "gpt-6.1-sol", providers.ProviderOpenAI)), "a pick the router did not serve funds like routed traffic")
	assert.False(t, pickedModelOnOwnPlan(withCodexPickFunding(creditFallback, "gpt-6-astra", providers.ProviderOpenAI)))
	assert.False(t, pickedModelOnOwnPlan(withCodexPickFunding(context.Background(), "gpt-6-astra", providers.ProviderOpenAI)), "launch models are not picks")
}

func TestPickedModelWithoutOwnLoginUsesManagedPool(t *testing.T) {
	leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "opaque-a", AccessToken: "token-a"}}}
	svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(leaser)

	_, lease, managed, err := svc.leaseManagedSubscription(auth.WithClientModelPassthrough(managedSubscriptionTestContext()), providers.ProviderAnthropic, "claude-opus-4-8")
	require.NoError(t, err)
	assert.True(t, managed)
	lease.Release()
}

func TestClientSelectedModelRoutesWhenNotServableAsIs(t *testing.T) {
	routed := router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeHaiku45.String(), Reason: "cluster_argmax"}
	envelope, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		request router.Request
	}{
		{name: "excluded", request: router.Request{
			RequestedModel:   catalog.ModelIDClaudeSonnet46.String(),
			EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
			ExcludedModels:   map[string]struct{}{catalog.ModelIDClaudeSonnet46.String(): {}},
		}},
		{name: "no gateway alias", request: router.Request{
			RequestedModel:   catalog.ModelIDClaudeSonnet46.String(),
			GatewayProviders: map[string]struct{}{providers.ProviderAnthropicGateway: {}},
		}},
		{name: "unknown model", request: router.Request{
			RequestedModel:   "claude-not-a-model",
			EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routerSpy := &blindExperimentRouterSpy{decision: routed}
			service := NewService(routerSpy, nil, nil, false, nil, newStubPinStore(), false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)

			turn, err := service.runTurnLoop(auth.WithClientModelPassthrough(context.Background()), envelope, envelope.RoutingFeatures(false), "api-key", uuid.New(), "", http.Header{}, tc.request)

			require.NoError(t, err)
			assert.False(t, turn.CallerModelPassthrough)
			assert.Equal(t, routed.Model, turn.Decision.Model)
			assert.Equal(t, 1, routerSpy.routeCalls)
		})
	}
}

func TestClientSelectedModelKeepsPolicyPassthroughStrict(t *testing.T) {
	routerSpy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
	service := NewService(routerSpy, nil, nil, false, nil, nil, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
	authService := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(routingPolicyStub{mode: auth.RoutingPolicyPassthrough}, nil)
	ctx, err := authService.WithRoutingPolicy(context.Background(), "installation")
	require.NoError(t, err)

	_, err = service.routeFor(auth.WithClientModelPassthrough(ctx), router.Request{
		RequestedModel:   catalog.ModelIDClaudeSonnet46.String(),
		EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		ExcludedModels:   map[string]struct{}{catalog.ModelIDClaudeSonnet46.String(): {}},
	})

	require.ErrorIs(t, err, cluster.ErrNoEligibleProvider)
	assert.Equal(t, 0, routerSpy.routeCalls)
}
