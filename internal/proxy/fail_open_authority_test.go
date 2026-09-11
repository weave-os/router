package proxy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
)

func TestDependencyFallbackUsesOriginalModelOutsideScoringRoster(t *testing.T) {
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"content":[]}`)) }}
	service := proxy.NewService(&fakeRouter{err: errors.New("policy unavailable")}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithAvailableModels(map[string]struct{}{"claude-haiku-4-5": {}}).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
	rec := httptest.NewRecorder()
	require.NoError(t, service.ProxyMessages(authedCtx("00000000-0000-0000-0000-000000000001"), []byte(pinTestBody), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	assert.Equal(t, "claude-opus-4-7", rec.Header().Get(proxy.HeaderRouterModel))
	assert.JSONEq(t, `{"content":[]}`, rec.Body.String())
	require.Len(t, provider.proxyBodies, 1)
	assert.JSONEq(t, pinTestBody, string(provider.proxyBodies[0]))
}

func TestDependencyFallbackCannotUseDeploymentKeyInByokOnlyMode(t *testing.T) {
	provider := &fakeProvider{}
	health := requestcontext.NewDependencyHealth()
	service := proxy.NewService(&fakeRouter{}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithByokOnly(true).
		WithDependencyFailOpen(health, requestcontext.DefaultPreparationLimits())
	ctx, state, _ := requestcontext.BeginPreparation(authedCtx("00000000-0000-0000-0000-000000000001"), health, requestcontext.DefaultPreparationLimits())
	defer state.Close()
	state.Fail(requestcontext.DependencyDatabase, errors.New("database unavailable"))
	err := service.ProxyMessages(ctx, []byte(pinTestBody), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	assert.ErrorIs(t, err, requestcontext.ErrDependencyUnavailable)
	assert.Empty(t, provider.proxyBodies)
}

func TestDependencyFallbackPreservesGatewayCredentialIsolation(t *testing.T) {
	gateway := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"gateway answer"}]}`))
	}}
	vendor := &fakeProvider{}
	service := proxy.NewService(&fakeRouter{err: errors.New("policy unavailable")}, map[string]providers.Client{providers.ProviderAnthropicGateway: gateway, providers.ProviderAnthropic: vendor}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithByokOnly(true).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
	ctx := context.WithValue(authedCtx("00000000-0000-0000-0000-000000000001"), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{Provider: providers.ProviderAnthropicGateway, Plaintext: []byte("gateway-only-key"), ModelAliases: map[string]string{"claude-opus-4-7": "gateway-opus"}}})
	rec := httptest.NewRecorder()
	require.NoError(t, service.ProxyMessages(ctx, []byte(pinTestBody), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	assert.JSONEq(t, `{"content":[{"type":"text","text":"gateway answer"}]}`, rec.Body.String())
	assert.Equal(t, providers.ProviderAnthropicGateway, rec.Header().Get(proxy.HeaderRouterProvider))
	require.Len(t, gateway.proxyCreds, 1)
	require.NotNil(t, gateway.proxyCreds[0])
	assert.Equal(t, "gateway-only-key", string(gateway.proxyCreds[0].APIKey))
	assert.Empty(t, vendor.proxyBodies)
}

func TestDependencyFallbackIsExcludedFromPolicyTraining(t *testing.T) {
	telemetry := newCaptureTelemetry()
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"content":[],"usage":{"input_tokens":12,"output_tokens":9}}`))
	}}
	service := proxy.NewService(&fakeRouter{err: errors.New("policy unavailable")}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", telemetry).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
	ctx := context.WithValue(authedCtx("00000000-0000-0000-0000-000000000001"), proxy.PolicyTrainingAllowedContextKey{}, true)
	require.NoError(t, service.ProxyMessages(ctx, []byte(pinTestBody), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	row := telemetry.firstRow(t)
	assert.False(t, row.TrainingAllowed)
	assert.Equal(t, "claude-opus-4-7", row.DecisionModel)
	assert.Equal(t, int32(12), row.InputTokens)
	assert.Equal(t, int32(9), row.OutputTokens)
	assert.Equal(t, "router.upstream", row.SpanType)
	require.NotNil(t, row.Inference)
	assert.Equal(t, inference.PurposeOriginalModelFallback, row.Inference.Purpose)
}
