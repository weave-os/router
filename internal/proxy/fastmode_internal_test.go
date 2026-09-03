package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	fastOpusModel = "claude-opus-5"
	fastLunaModel = "gpt-5.6-luna"
)

func fastModeCtx(models ...string) context.Context {
	return context.WithValue(context.Background(), InstallationFastModeModelsContextKey{}, models)
}

func newFastModeService(decision router.Decision, upstream providers.Client) (*Service, *bypassCaptureTelemetry) {
	telemetry := newBypassCaptureTelemetry()
	svc := NewService(staticRouter{decision: decision}, map[string]providers.Client{
		providers.ProviderAnthropic:  upstream,
		providers.ProviderOpenAI:     upstream,
		providers.ProviderOpenRouter: upstream,
	}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", telemetry)
	return svc, telemetry
}

func TestFastModeForAttempt(t *testing.T) {
	t.Run("off when the installation did not opt the model in", func(t *testing.T) {
		assert.False(t, fastModeForAttempt(fastModeCtx(fastLunaModel), fastOpusModel, providers.ProviderAnthropic))
		assert.False(t, fastModeForAttempt(context.Background(), fastOpusModel, providers.ProviderAnthropic))
	})
	t.Run("on for an opted-in first-party binding", func(t *testing.T) {
		assert.True(t, fastModeForAttempt(fastModeCtx(fastOpusModel), fastOpusModel, providers.ProviderAnthropic))
		assert.True(t, fastModeForAttempt(fastModeCtx(fastLunaModel), fastLunaModel, providers.ProviderOpenAI))
	})
	t.Run("off when the serving binding has no fast tier", func(t *testing.T) {
		assert.False(t, fastModeForAttempt(fastModeCtx(fastOpusModel), fastOpusModel, providers.ProviderOpenRouter))
		assert.False(t, fastModeForAttempt(fastModeCtx("claude-sonnet-4-6"), "claude-sonnet-4-6", providers.ProviderAnthropic))
	})
	t.Run("off when the turn is served on a subscription", func(t *testing.T) {
		ctx := context.WithValue(fastModeCtx(fastOpusModel), CredentialsContextKey{}, &Credentials{
			APIKey: []byte("sk-ant-oat-token"),
			Source: credSourceSubscription,
			OAuth:  true,
		})
		assert.False(t, fastModeForAttempt(ctx, fastOpusModel, providers.ProviderAnthropic))
	})
}

func TestServedPricing(t *testing.T) {
	base, ok := catalog.PriceFor(providers.ProviderAnthropic, fastOpusModel)
	require.True(t, ok)
	fast, ok := catalog.FastPriceFor(providers.ProviderAnthropic, fastOpusModel)
	require.True(t, ok)

	got, ok := servedPricing(providers.ProviderAnthropic, fastOpusModel, true)
	require.True(t, ok)
	assert.Equal(t, fast, got)

	got, ok = servedPricing(providers.ProviderAnthropic, fastOpusModel, false)
	require.True(t, ok)
	assert.Equal(t, base, got)

	gatewayBase, ok := catalog.PriceFor(providers.ProviderOpenRouter, "qwen/qwen3-235b-a22b-2507")
	require.True(t, ok)
	got, ok = servedPricing(providers.ProviderOpenRouter, "qwen/qwen3-235b-a22b-2507", true)
	require.True(t, ok, "a binding without a fast tier must still price at its list rate")
	assert.Equal(t, gatewayBase, got)
}

func TestProxyMessages_FastModeAnthropicDispatchesFastAndBillsFastRate(t *testing.T) {
	const (
		inputTokens  = 1200
		outputTokens = 340
	)
	upstream := &bypassFakeProvider{respBody: `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":1200,"output_tokens":340}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderAnthropic, Model: fastOpusModel}, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyMessages(fastModeCtx(fastOpusModel), body, rec, req))

	require.Equal(t, 1, upstream.dispatches)
	assert.Equal(t, "fast", gjson.GetBytes(upstream.capturedBody, "speed").Str)

	fast, ok := catalog.FastPriceFor(providers.ProviderAnthropic, fastOpusModel)
	require.True(t, ok)
	want := routerResponseCostFromPricing(fast, providers.ProviderAnthropic, inputTokens, outputTokens, 0, 0)
	assert.Equal(t, strconv.FormatFloat(want.TotalUSD, 'f', -1, 64), rec.Header().Get(HeaderRouterCostUSD))
}

func TestProxyMessages_FastModeOffLeavesRequestAndListPrice(t *testing.T) {
	const (
		inputTokens  = 1200
		outputTokens = 340
	)
	upstream := &bypassFakeProvider{respBody: `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":1200,"output_tokens":340}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderAnthropic, Model: fastOpusModel}, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyMessages(fastModeCtx(fastLunaModel), body, rec, req))

	require.Equal(t, 1, upstream.dispatches)
	assert.False(t, gjson.GetBytes(upstream.capturedBody, "speed").Exists())

	base, ok := catalog.PriceFor(providers.ProviderAnthropic, fastOpusModel)
	require.True(t, ok)
	want := routerResponseCostFromPricing(base, providers.ProviderAnthropic, inputTokens, outputTokens, 0, 0)
	assert.Equal(t, strconv.FormatFloat(want.TotalUSD, 'f', -1, 64), rec.Header().Get(HeaderRouterCostUSD))
}

func TestProxyMessages_FastModeOpenAICrossFormatSetsPriorityTier(t *testing.T) {
	upstream := &bypassFakeProvider{respBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-5.6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderOpenAI, Model: fastLunaModel}, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyMessages(fastModeCtx(fastLunaModel), body, rec, req))

	require.Equal(t, 1, upstream.dispatches)
	assert.Equal(t, "priority", gjson.GetBytes(upstream.capturedBody, "service_tier").Str)
}

func TestProxyOpenAIChatCompletion_FastModeSetsPriorityTier(t *testing.T) {
	upstream := &bypassFakeProvider{respBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-5.6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderOpenAI, Model: fastLunaModel}, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyOpenAIChatCompletion(fastModeCtx(fastLunaModel), body, rec, req))

	require.Equal(t, 1, upstream.dispatches)
	assert.Equal(t, "priority", gjson.GetBytes(upstream.capturedBody, "service_tier").Str)
}

func TestProxyMessages_FastModeGatewayNeverGetsFastFields(t *testing.T) {
	upstream := &bypassFakeProvider{respBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"qwen/qwen3-235b-a22b-2507","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderOpenRouter, Model: "qwen/qwen3-235b-a22b-2507"}, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyMessages(fastModeCtx("qwen/qwen3-235b-a22b-2507", fastOpusModel), body, rec, req))

	require.Equal(t, 1, upstream.dispatches)
	assert.False(t, gjson.GetBytes(upstream.capturedBody, "service_tier").Exists())
	assert.False(t, gjson.GetBytes(upstream.capturedBody, "speed").Exists())
}
