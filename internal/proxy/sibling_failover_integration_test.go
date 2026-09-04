package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// statusOverloaded is Anthropic's upstream-overload status; net/http has no constant for it.
const statusOverloaded = 529

const overloadedSSE = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

// siblingClusterDecision routes to an Anthropic model that shares its cluster
// with one Fireworks-served candidate.
func siblingClusterDecision(reason string) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-4-8",
		Reason:   reason,
		Metadata: &router.RoutingMetadata{
			PolicyGroup:     "cluster-7",
			CandidateModels: []string{"claude-opus-4-8", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"claude-opus-4-8":          providers.ProviderAnthropic,
				"deepseek/deepseek-v4-pro": providers.ProviderFireworks,
			},
		},
	}
}

// TestProxyMessages_OverloadedModelDegradesToSameClusterCandidate: a sole-binding
// Anthropic model 529s on every attempt; the turn must re-dispatch the next
// policy candidate rather than surfacing the overload to the client.
func TestProxyMessages_OverloadedModelDegradesToSameClusterCandidate(t *testing.T) {
	var (
		mu             sync.Mutex
		anthropicCount int
		fireworksCount int
		fireworksModel string
	)

	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		anthropicCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(overloadedSSE))
	}))
	defer anthropicUpstream.Close()

	fireworks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		fireworksCount++
		fireworksModel = gjson.GetBytes(body, "model").String()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, c := range []string{
			`data: {"id":"fw-1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"fw-1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = w.Write([]byte(c))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer fireworks.Close()

	store := newFakePinStore()
	svc := proxy.NewService(
		&fakeRouter{decision: siblingClusterDecision("")},
		map[string]providers.Client{
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", anthropicUpstream.URL),
			providers.ProviderFireworks: openaicompat.NewClient("test-fw-key", fireworks.URL),
		},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", newCaptureTelemetry(),
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAnthropic: {},
		providers.ProviderFireworks: {},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.NoError(t, err, "ProxyMessages should succeed via the same-cluster candidate")

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, anthropicCount, 1, "the overloaded model exhausts its same-binding retries first")
	assert.Equal(t, 1, fireworksCount, "the same-cluster candidate is dispatched once")
	assert.Equal(t, "deepseek/deepseek-v4-pro", fireworksModel,
		"the candidate request must be re-emitted for the candidate, not the overloaded model")

	respBody := rec.Body.String()
	assert.Contains(t, respBody, "event: message_start", "client sees the candidate's stream")
	assert.Contains(t, respBody, "event: message_stop")
	assert.NotContains(t, respBody, "overloaded_error", "the upstream overload must not reach the client")
	assert.Equal(t, providers.ProviderFireworks, rec.Header().Get(proxy.HeaderRouterProvider))
	assert.Equal(t, "deepseek/deepseek-v4-pro", rec.Header().Get(proxy.HeaderRouterModel))

	require.NotEmpty(t, store.usages, "the served candidate must be recorded on the pin")
	assert.Equal(t, "deepseek/deepseek-v4-pro", store.usages[len(store.usages)-1].ServedModel)
}

// TestProxyMessages_OverloadAfterCommitKeepsServingModel: once the first
// upstream bytes are on the wire the turn is committed to its model, so a
// mid-stream overload must be rendered in-stream rather than answered by a
// second model whose output would interleave with the first's.
func TestProxyMessages_OverloadAfterCommitKeepsServingModel(t *testing.T) {
	var (
		mu             sync.Mutex
		fireworksCount int
	)
	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")
		_, _ = io.WriteString(w, overloadedSSE)
	}))
	defer anthropicUpstream.Close()

	fireworks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		fireworksCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer fireworks.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: siblingClusterDecision("")},
		map[string]providers.Client{
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", anthropicUpstream.URL),
			providers.ProviderFireworks: openaicompat.NewClient("test-fw-key", fireworks.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAnthropic: {},
		providers.ProviderFireworks: {},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	_ = svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)

	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, fireworksCount, "no model switch once the response is committed")
	assert.Contains(t, rec.Body.String(), "partial", "the committed stream is preserved")
	assert.Equal(t, "claude-opus-4-8", rec.Header().Get(proxy.HeaderRouterModel))
}

// TestProxyMessages_ForceModelOverloadDoesNotDegrade: an explicit /force-model
// request asked for one model specifically; silently serving another would
// answer a question the user didn't ask.
func TestProxyMessages_ForceModelOverloadDoesNotDegrade(t *testing.T) {
	var (
		mu             sync.Mutex
		fireworksCount int
	)
	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(overloadedSSE))
	}))
	defer anthropicUpstream.Close()

	fireworks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		fireworksCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer fireworks.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: siblingClusterDecision(translate.ReasonUserForceModel)},
		map[string]providers.Client{
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", anthropicUpstream.URL),
			providers.ProviderFireworks: openaicompat.NewClient("test-fw-key", fireworks.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAnthropic: {},
		providers.ProviderFireworks: {},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)

	require.Error(t, err, "the forced model's overload surfaces instead of degrading")
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, fireworksCount, "a forced model is never swapped out")
	assert.Contains(t, rec.Body.String(), "overloaded_error")
}

// TestProxyMessages_OverloadWithoutCandidatesSurfacesUpstreamError: with no
// same-cluster candidate the deferred upstream error must still reach the
// client — a rescue that can't run must never swallow the failure.
func TestProxyMessages_OverloadWithoutCandidatesSurfacesUpstreamError(t *testing.T) {
	var mu sync.Mutex
	anthropicCount := 0
	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		anthropicCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}))
	defer anthropicUpstream.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "claude-opus-4-8",
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"claude-opus-4-8"}},
		}},
		map[string]providers.Client{
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", anthropicUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.Error(t, err, "no candidate to degrade to, so the overload surfaces")

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, anthropicCount, 1, "same-binding retries still run")
	assert.Contains(t, rec.Body.String(), "Overloaded", "the deferred upstream error must reach the client")
}

// TestProxyMessages_SubscriptionOverloadSurfacesOnceAfterRetry: a customer
// credential binds the turn to its provider, so sibling rescue is ineligible;
// the Weave-key retry runs and the overload surfaces exactly once.
func TestProxyMessages_SubscriptionOverloadSurfacesOnceAfterRetry(t *testing.T) {
	var (
		mu             sync.Mutex
		anthropicCount int
		fireworksCount int
	)

	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		anthropicCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(overloadedSSE))
	}))
	defer anthropicUpstream.Close()

	fireworks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		fireworksCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer fireworks.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: siblingClusterDecision("")},
		map[string]providers.Client{
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", anthropicUpstream.URL),
			providers.ProviderFireworks: openaicompat.NewClient("test-fw-key", fireworks.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAnthropic: {},
		providers.ProviderFireworks: {},
	})

	ctx := context.WithValue(
		authedCtx("11111111-1111-1111-1111-111111111111"),
		proxy.AnthropicSubscriptionContextKey{},
		"sk-ant-oat01-subscription-token",
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(ctx, body, rec, req)
	require.Error(t, err, "a customer-credentialed turn has nowhere to degrade to")

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, anthropicCount, 1, "the subscription retry reruns the same model on the Weave key")
	assert.Zero(t, fireworksCount, "a subscription credential binds the turn to its own provider")
	assert.Equal(t, 1, strings.Count(rec.Body.String(), "overloaded_error"),
		"the deferred overload is flushed exactly once across the rescue chain")
}
