package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func priorModelContext(arm auth.BlindExperimentArm, clientApp, priorHeader string) context.Context {
	h := http.Header{}
	h.Set("X-App", clientApp)
	h.Set(PriorModelHeader, priorHeader)
	return context.WithValue(blindExperimentContext(arm), ClientIdentityContextKey{}, ClientIdentityFromHeaders(h))
}

func TestClientIdentityParsesPriorModelHeader(t *testing.T) {
	for raw, want := range map[string]string{
		"claude-sonnet-4-6":      "claude-sonnet-4-6",
		" gpt-5.4 ":              "gpt-5.4",
		"claude-opus-4-7[1m]":    "claude-opus-4-7",
		"z-ai/glm-5.1":           "z-ai/glm-5.1",
		"unset":                  "",
		"weave-auto":             "",
		"weave-auto[1m]":         "",
		"claude sonnet; rm -rf ": "",
		"-flag":                  "",
		"":                       "",
	} {
		h := http.Header{}
		h.Set(PriorModelHeader, raw)
		assert.Equal(t, want, ClientIdentityFromHeaders(h).PriorModel, "header %q", raw)
	}
}

func TestPassthroughArmServesRecordedPriorModel(t *testing.T) {
	service := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	enabled := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}}
	for clientApp, prior := range map[string]string{ClientAppClaudeCode: "claude-sonnet-4-6", ClientAppCodex: "gpt-5.4"} {
		ctx := priorModelContext(auth.BlindExperimentArmPassthrough, clientApp, prior)

		decision, passthrough, err := service.blindExperimentPassthroughDecision(ctx,
			router.Request{RequestedModel: "weave-auto", EnabledProviders: enabled})

		require.NoError(t, err)
		assert.True(t, passthrough)
		assert.Equal(t, prior, decision.Model, "%s passthrough must serve the model recorded at install", clientApp)
		assert.Equal(t, blindExperimentPublicDecisionReason, decision.Reason, "the arm must stay undisclosed")
	}
}

func TestPassthroughArmFallsBackToClientDefaultWhenPriorModelCannotServe(t *testing.T) {
	service := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	anthropicOnly := map[string]struct{}{providers.ProviderAnthropic: {}}
	for _, tc := range []struct {
		name  string
		prior string
		ctx   func(context.Context) context.Context
		req   router.Request
	}{
		{name: "unrecorded", prior: "unset", req: router.Request{EnabledProviders: anthropicOnly}},
		{name: "unknown model", prior: "claude-retired-9", req: router.Request{EnabledProviders: anthropicOnly}},
		{name: "no provider", prior: "gpt-5.4", req: router.Request{EnabledProviders: anthropicOnly}},
		{name: "excluded", prior: "claude-sonnet-4-6", req: router.Request{EnabledProviders: anthropicOnly,
			ExcludedModels: map[string]struct{}{"claude-sonnet-4-6": {}}}},
		{name: "safety excluded", prior: "claude-sonnet-4-6", req: router.Request{EnabledProviders: anthropicOnly,
			SafetyExcludedModels: map[string]struct{}{"claude-sonnet-4-6": {}}}},
		{name: "text-only model on an image turn", prior: "z-ai/glm-5.1", req: router.Request{HasImages: true}},
		{name: "outside the allowlist", prior: "claude-sonnet-4-6", req: router.Request{EnabledProviders: anthropicOnly},
			ctx: func(ctx context.Context) context.Context {
				return context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{"claude-opus-5-5"})
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := priorModelContext(auth.BlindExperimentArmPassthrough, ClientAppClaudeCode, tc.prior)
			if tc.ctx != nil {
				ctx = tc.ctx(ctx)
			}
			tc.req.RequestedModel = "weave-auto"

			decision, _, err := service.blindExperimentPassthroughDecision(ctx, tc.req)

			require.NoError(t, err)
			assert.Equal(t, "claude-opus-5-5", decision.Model)

			params := InsertTelemetryParams{RequestedModel: "weave-auto", DecisionModel: decision.Model}
			applyBlindExperimentTelemetry(ctx, &params, &turnLoopResult{Decision: decision, CallerModelPassthrough: true})
			assert.Equal(t, "claude-opus-5-5", params.BlindExperimentBaselineModel)
			assert.Equal(t, auth.BlindExperimentBaselineSourceClientDefault, params.BlindExperimentBaselineSource)
			require.NotNil(t, params.BlindExperimentModelChanged)
			assert.False(t, *params.BlindExperimentModelChanged)
		})
	}
}

func TestPriorModelDoesNotOverrideAnExplicitPick(t *testing.T) {
	service := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	ctx := priorModelContext(auth.BlindExperimentArmPassthrough, ClientAppClaudeCode, "claude-sonnet-4-6")

	decision, _, err := service.blindExperimentPassthroughDecision(ctx, router.Request{
		RequestedModel: "claude-opus-4-7", EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}})

	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", decision.Model)
}

func TestBlindExperimentTelemetryRecordsPriorInstallBaseline(t *testing.T) {
	routed := router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5", Reason: "cluster_argmax"}
	for _, tc := range []struct {
		name      string
		arm       auth.BlindExperimentArm
		requested string
		routed    *turnLoopResult
		baseline  string
		source    auth.BlindExperimentBaselineSource
		changed   bool
	}{
		{name: "passthrough served the prior model", arm: auth.BlindExperimentArmPassthrough, requested: "weave-auto",
			routed:   &turnLoopResult{Decision: router.Decision{Model: "claude-sonnet-4-6"}, CallerModelPassthrough: true},
			baseline: "claude-sonnet-4-6", source: auth.BlindExperimentBaselineSourcePriorInstall},
		{name: "router_on compares against the prior model", arm: auth.BlindExperimentArmRouterOn, requested: "weave-auto",
			routed:   &turnLoopResult{Decision: routed},
			baseline: "claude-sonnet-4-6", source: auth.BlindExperimentBaselineSourcePriorInstall, changed: true},
		{name: "a named model is its own baseline", arm: auth.BlindExperimentArmRouterOn, requested: "claude-opus-4-7",
			routed:   &turnLoopResult{Decision: routed},
			baseline: "claude-opus-4-7", source: auth.BlindExperimentBaselineSourceRequestedModel, changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := priorModelContext(tc.arm, ClientAppClaudeCode, "claude-sonnet-4-6")
			params := InsertTelemetryParams{RequestedModel: tc.requested, DecisionModel: tc.routed.Decision.Model}

			applyBlindExperimentTelemetry(ctx, &params, tc.routed)

			assert.Equal(t, tc.baseline, params.BlindExperimentBaselineModel)
			assert.Equal(t, tc.source, params.BlindExperimentBaselineSource)
			require.NotNil(t, params.BlindExperimentModelChanged)
			assert.Equal(t, tc.changed, *params.BlindExperimentModelChanged)
		})
	}
}
