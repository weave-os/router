package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"

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

// fastQuotaFakeProvider refuses every fast-tier body with Anthropic's
// fast-mode quota 429 and answers standard-speed bodies with respBody,
// recording each dispatched body in order.
type fastQuotaFakeProvider struct {
	respBody string
	bodies   [][]byte
}

const anthropicFastQuota429 = `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your organization's rate limit of 0 fast mode input tokens per minute."}}`

func (f *fastQuotaFakeProvider) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	f.bodies = append(f.bodies, append([]byte(nil), prep.Body...))
	if gjson.GetBytes(prep.Body, "speed").Str == "fast" {
		return &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(anthropicFastQuota429)}
	}
	_, _ = io.WriteString(w, f.respBody)
	return nil
}

func (f *fastQuotaFakeProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func TestProxyMessages_FastModeQuotaRejectionRetriesAtStandardSpeedAndBillsListPrice(t *testing.T) {
	const (
		inputTokens  = 1200
		outputTokens = 340
	)
	upstream := &fastQuotaFakeProvider{respBody: `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":1200,"output_tokens":340}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderAnthropic, Model: fastOpusModel}, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyMessages(fastModeCtx(fastOpusModel), body, rec, req))

	require.Len(t, upstream.bodies, 2, "one fast send, one standard-speed retry")
	assert.Equal(t, "fast", gjson.GetBytes(upstream.bodies[0], "speed").Str)
	assert.False(t, gjson.GetBytes(upstream.bodies[1], "speed").Exists(), "retry must drop the fast tier")
	assert.Equal(t, http.StatusOK, rec.Code)

	base, ok := catalog.PriceFor(providers.ProviderAnthropic, fastOpusModel)
	require.True(t, ok)
	want := routerResponseCostFromPricing(base, providers.ProviderAnthropic, inputTokens, outputTokens, 0, 0)
	assert.Equal(t, strconv.FormatFloat(want.TotalUSD, 'f', -1, 64), rec.Header().Get(HeaderRouterCostUSD), "a turn served at standard speed bills at list price")
}

func TestProxyOpenAIChatCompletion_FastModeQuotaRejectionRetriesAtStandardSpeed(t *testing.T) {
	upstream := &fastQuotaFakeProvider{respBody: `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderAnthropic, Model: fastOpusModel}, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyOpenAIChatCompletion(fastModeCtx(fastOpusModel), body, rec, req))

	require.Len(t, upstream.bodies, 2)
	assert.Equal(t, "fast", gjson.GetBytes(upstream.bodies[0], "speed").Str)
	assert.False(t, gjson.GetBytes(upstream.bodies[1], "speed").Exists())
	assert.Equal(t, http.StatusOK, rec.Code)
}

// An ordinary 429 on a fast send is not a quota rejection: the failover loop
// owns it and every retry stays on the fast tier.
func TestAnthropicTierAttempt_OrdinaryRateLimitStaysFast(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	opts := translate.EmitOptions{
		TargetModel:    fastOpusModel,
		TargetProvider: providers.ProviderAnthropic,
		Capabilities:   router.Lookup(fastOpusModel),
		FastMode:       true,
	}
	prep, err := env.PrepareAnthropic(req.Header, opts)
	require.NoError(t, err)

	rateLimited := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your organization's rate limit of 50 requests per minute."}}`)}
	upstream := &bypassFakeProvider{proxyErr: rateLimited}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderAnthropic, Model: fastOpusModel}, upstream)
	tiered := &anthropicTierAttempt{
		s:             svc,
		log:           slog.Default(),
		env:           env,
		r:             req,
		opts:          opts,
		native:        svc.anthropicNativeAttempt(env, req, prep, httptest.NewRecorder(), nil, "", func(*otel.UsageExtractor) {}, func(router.Decision, bool) {}),
		sink:          httptest.NewRecorder(),
		setExtractor:  func(*otel.UsageExtractor) {},
		setStreamCost: func(router.Decision, bool) {},
		logBody:       func(router.Decision, []byte) { t.Fatal("an ordinary rate limit must not re-emit the body") },
	}
	var served []bool
	_, err = tiered.dispatch(fastModeCtx(fastOpusModel), router.Decision{Provider: providers.ProviderAnthropic, Model: fastOpusModel}, upstream, func(fast bool) { served = append(served, fast) })
	require.ErrorIs(t, err, rateLimited)
	assert.Equal(t, 1, upstream.dispatches)
	assert.Equal(t, []bool{true}, served)
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

// A baseline/subscription failover builds one Anthropic body up front and then
// walks the binding list; a hop from first-party Anthropic onto a gateway must
// re-emit without the fast tier and report the attempt as not fast.
func TestAnthropicTierAttempt_ReemitsWhenBindingLosesFastTier(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	opts := translate.EmitOptions{
		TargetModel:    fastOpusModel,
		TargetProvider: providers.ProviderAnthropic,
		Capabilities:   router.Lookup(fastOpusModel),
		FastMode:       true,
	}
	prep, err := env.PrepareAnthropic(req.Header, opts)
	require.NoError(t, err)
	require.Equal(t, "fast", gjson.GetBytes(prep.Body, "speed").Str)

	upstream := &bypassFakeProvider{respBody: `{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderAnthropic, Model: fastOpusModel}, upstream)
	var loggedBodies [][]byte
	tiered := &anthropicTierAttempt{
		s:             svc,
		log:           slog.Default(),
		env:           env,
		r:             req,
		opts:          opts,
		native:        svc.anthropicNativeAttempt(env, req, prep, httptest.NewRecorder(), nil, "", func(*otel.UsageExtractor) {}, func(router.Decision, bool) {}),
		sink:          httptest.NewRecorder(),
		setExtractor:  func(*otel.UsageExtractor) {},
		setStreamCost: func(router.Decision, bool) {},
		logBody:       func(_ router.Decision, body []byte) { loggedBodies = append(loggedBodies, body) },
	}
	var served []bool
	attempt := tiered.attempt(func(fast bool) { served = append(served, fast) })
	ctx := fastModeCtx(fastOpusModel)

	require.NoError(t, attempt(ctx, router.Decision{Provider: providers.ProviderAnthropic, Model: fastOpusModel}, upstream))
	assert.Equal(t, "fast", gjson.GetBytes(upstream.capturedBody, "speed").Str)
	assert.Empty(t, loggedBodies, "same tier reuses the prepared body")

	require.NoError(t, attempt(ctx, router.Decision{Provider: providers.ProviderAnthropicGateway, Model: fastOpusModel}, upstream))
	assert.False(t, gjson.GetBytes(upstream.capturedBody, "speed").Exists(), "gateway hop must not carry the fast-tier speed field")
	assert.Len(t, loggedBodies, 1, "tier change re-emits the body once")

	assert.Equal(t, []bool{true, false}, served)
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
