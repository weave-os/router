package proxy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/google"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
)

const recoveredResponsesFixture = `{"id":"resp_test","object":"response","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"Recovered answer","annotations":[]}]}],"usage":{"input_tokens":8,"output_tokens":3}}`

func TestPolicyFaultRecoveryAcrossServingSurfaces(t *testing.T) {
	for _, surface := range []struct {
		name, provider, primaryModel, alternativeModel, body, response string
		client                                                         func(string, string) providers.Client
		serve                                                          func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", providers.ProviderAnthropic, "claude-haiku-4-5", "claude-sonnet-4-6", `{"model":"claude-opus-4-8","max_tokens":2048,"messages":[{"role":"user","content":"Explain the synthetic task"}]}`, `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"Recovered answer"}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":3}}`, func(k, u string) providers.Client { return anthropic.NewClient(k, u) }, (*proxy.Service).ProxyMessages},
		{"chat", providers.ProviderOpenAI, "gpt-5.6-luna", "gpt-5.6-sol", `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Explain the synthetic task"}]}`, `{"id":"chat_test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Recovered answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":3}}`, func(k, u string) providers.Client { return openaicompat.NewClient(k, u) }, (*proxy.Service).ProxyOpenAIChatCompletion},
		{"responses", providers.ProviderOpenAI, "gpt-5.6-luna", "gpt-5.6-sol", `{"model":"gpt-5.6-sol","input":"Explain the synthetic task"}`, recoveredResponsesFixture, func(k, u string) providers.Client { return openaicompat.NewClient(k, u) }, (*proxy.Service).ProxyOpenAIResponses},
		{"gemini", providers.ProviderGoogle, "gemini-3.5-flash-lite", "gemini-3.5-flash", `{"model":"gemini-3.5-flash","contents":[{"role":"user","parts":[{"text":"Explain the synthetic task"}]}]}`, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Recovered answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}`, func(k, u string) providers.Client { return google.NewNativeClient(k, u) }, (*proxy.Service).ProxyGeminiGenerateContent},
	} {
		for _, fault := range []struct {
			name   string
			status int
			body   string
		}{
			{"rejected evidence", 400, `{"error":"raw V5 live conversation_messages has no usable user boundary"}`},
			{"malformed output", 200, `{broken`},
			{"unauthorized dependency", 401, `{"error":"unauthorized"}`},
		} {
			t.Run(surface.name+"/"+fault.name, func(t *testing.T) {
				var classifierCalls, providerCalls atomic.Int64
				sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					classifierCalls.Add(1)
					w.WriteHeader(fault.status)
					_, _ = io.WriteString(w, fault.body)
				}))
				defer sidecar.Close()
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					model := gjson.GetBytes(body, "model").String()
					if surface.provider == providers.ProviderGoogle {
						model = strings.Split(strings.TrimPrefix(r.URL.Path, "/v1beta/models/"), ":")[0]
					}
					providerCalls.Add(1)
					if model == surface.primaryModel {
						w.WriteHeader(http.StatusNotFound)
						_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","code":"model_not_found","message":"model not found"}}`)
						return
					}
					assert.Equal(t, surface.alternativeModel, model)
					w.Header().Set("Content-Type", "application/json")
					if surface.provider == providers.ProviderOpenAI {
						assert.True(t, strings.HasSuffix(r.URL.Path, "/responses"), r.URL.Path)
						if gjson.GetBytes(body, "stream").Bool() {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Recovered answer\"}\n\n")
							_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":"+recoveredResponsesFixture+"}\n\n")
							return
						}
						_, _ = io.WriteString(w, recoveredResponsesFixture)
					} else {
						_, _ = io.WriteString(w, surface.response)
					}
				}))
				defer upstream.Close()
				availableProviders := map[string]struct{}{surface.provider: {}}
				service := proxy.NewService(nil, map[string]providers.Client{surface.provider: surface.client("synthetic-key", upstream.URL)}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
					WithHMMRouter(hmm.New(policyclient.New(sidecar.URL, sidecar.Client(), time.Second), availableProviders)).
					WithAvailableModels(map[string]struct{}{surface.primaryModel: {}, surface.alternativeModel: {}}).
					WithDeploymentKeyedProviders(availableProviders).
					WithPolicyDeadlineDefaultModel(surface.primaryModel)
				ctx := router.WithStrategy(authedCtx("11111111-1111-1111-1111-111111111111"), router.StrategyHMM)
				ctx = observability.WithLogger(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/v1/"+surface.name, strings.NewReader(surface.body))
				err := surface.serve(service, ctx, []byte(surface.body), response, request)
				require.NoError(t, err)
				assert.Equal(t, http.StatusOK, response.Code)
				assert.Contains(t, response.Body.String(), "Recovered answer")
				assert.Equal(t, surface.alternativeModel, response.Header().Get(proxy.HeaderRouterModel))
				wantCalls := int64(2)
				if surface.provider == providers.ProviderOpenAI {
					wantCalls = 3
				} // Includes the adapter’s bounded /v1 discovery probe on 404.
				assert.Equal(t, wantCalls, providerCalls.Load(), fmt.Sprintf("response: %s", response.Body.String()))
				assert.Equal(t, int64(1), classifierCalls.Load())
			})
		}
	}
}

func TestCompactedToolLoopRetainsOriginalClassifierEvidence(t *testing.T) {
	const task = "Investigate the synthetic parser and repair its boundary handling"
	messages := []any{map[string]any{"role": "user", "content": task}}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("call_%d", i)
		messages = append(messages,
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", 70000)}, map[string]any{"type": "tool_use", "id": id, "name": "read_file", "input": map[string]string{"path": "synthetic.go"}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": strings.Repeat("y", 1000)}}})
	}
	body, err := json.Marshal(map[string]any{"model": "claude-opus-4-8", "max_tokens": 2048, "messages": messages})
	require.NoError(t, err)
	var classifierCalls, providerCalls atomic.Int64
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		classifierCalls.Add(1)
		wire, _ := io.ReadAll(r.Body)
		assert.Contains(t, gjson.GetBytes(wire, "conversation_messages").Raw, task)
		assert.Len(t, gjson.GetBytes(wire, "conversation_messages").Array(), 96, "projection must use original 101 messages, not the compacted tail")
		assert.True(t, gjson.GetBytes(wire, "history_truncated").Bool())
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"raw V5 live conversation_messages has no usable user boundary"}`)
	}))
	defer sidecar.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		wire, _ := io.ReadAll(r.Body)
		retained := gjson.GetBytes(wire, "messages").Array()
		assert.LessOrEqual(t, len(retained), 14, "the 12-message rescue must actually run")
		assert.Contains(t, string(wire), task)
		calls := map[string]bool{}
		for _, message := range retained {
			for _, block := range message.Get("content").Array() {
				if block.Get("type").String() == "tool_use" {
					calls[block.Get("id").String()] = true
				}
				if block.Get("type").String() == "tool_result" {
					assert.True(t, calls[block.Get("tool_use_id").String()], "every retained result needs its call")
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"Recovered answer"}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":3}}`)
	}))
	defer upstream.Close()
	available := map[string]struct{}{providers.ProviderAnthropic: {}}
	service := proxy.NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("synthetic-key", upstream.URL)}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithHMMRouter(hmm.New(policyclient.New(sidecar.URL, sidecar.Client(), time.Second), available)).
		WithAvailableModels(map[string]struct{}{"claude-haiku-4-5": {}}).WithDeploymentKeyedProviders(available).WithCompaction(nil, 0.8)
	ctx := observability.WithLogger(router.WithStrategy(authedCtx("11111111-1111-1111-1111-111111111111"), router.StrategyHMM), slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := httptest.NewRecorder()
	err = service.ProxyMessages(ctx, body, response, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.NoError(t, err)
	assert.Contains(t, response.Body.String(), "Recovered answer")
	assert.Equal(t, int64(1), classifierCalls.Load())
	assert.Equal(t, int64(1), providerCalls.Load())
}

func TestRecoveryStreamingRespectsCommitAndRecordsOnlyServedHistory(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		primarySSE     string
		secondaryFails bool
		wantSecondary  int64
		wantHistory    bool
		codex          bool
	}{
		{"failure before output recovers across providers", `event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"synthetic overload"}}` + "\n\n", false, 1, true, false},
		{"committed output prevents recovery", strings.Split(anthropicRescueSSE, "event: content_block_stop")[0], false, 0, false, false},
		{"exhaustion preserves prior history", `event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"synthetic overload"}}` + "\n\n", true, 1, false, false},
		{"Codex recovery translates the winning Responses stream", `event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"synthetic overload"}}` + "\n\n", false, 1, true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var secondaryCalls atomic.Int64
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if scenario.wantSecondary == 0 {
					w.Header().Set("Content-Length", strconv.Itoa(len(scenario.primarySSE)+1000))
				}
				_, _ = io.WriteString(w, scenario.primarySSE)
			}))
			defer primary.Close()
			secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondaryCalls.Add(1)
				if scenario.secondaryFails {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"synthetic terminal failure","type":"invalid_request_error"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Recovered answer\"}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":"+recoveredResponsesFixture+"}\n\n")
			}))
			defer secondary.Close()
			available := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}}
			store := newFakePinStore()
			ledger := &capturingBillingRepo{}
			service := proxy.NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("synthetic-key", primary.URL), providers.ProviderOpenAI: openaicompat.NewClient("synthetic-key", secondary.URL)}, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", newCaptureTelemetry()).
				WithAvailableModels(map[string]struct{}{"claude-haiku-4-5": {}, "gpt-5.6-luna": {}}).WithDeploymentKeyedProviders(available).
				WithPolicyDeadlineDefaultModel("claude-haiku-4-5").WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: &fakeRouter{err: fmt.Errorf("synthetic dependency failure: %w", hmm.ErrHMMUnavailable)}}).
				WithBillingService(billing.NewService(ledger))
			ctx := observability.WithLogger(router.WithStrategy(authedCtx("11111111-1111-1111-1111-111111111111"), router.StrategyHMM), slog.New(slog.NewTextHandler(io.Discard, nil)))
			ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, "synthetic-billing-org")
			ctx = context.WithValue(ctx, proxy.AnthropicSubscriptionContextKey{}, "sk-ant-oat01-synthetic-subscription")
			body := `{"model":"claude-opus-4-8","max_tokens":2048,"stream":true,"messages":[{"role":"user","content":"Explain the synthetic task"}]}`
			response := httptest.NewRecorder()
			var err error
			if scenario.codex {
				ctx = context.WithValue(ctx, proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
				err = service.ProxyOpenAIResponses(ctx, []byte(responsesTurnBody), response, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			} else {
				err = service.ProxyMessages(ctx, []byte(body), response, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			}
			assert.Equal(t, scenario.wantSecondary, secondaryCalls.Load(), response.Body.String())
			if scenario.wantHistory {
				require.NoError(t, err)
				assert.Contains(t, response.Body.String(), "Recovered answer")
				assert.NotContains(t, response.Body.String(), "synthetic overload")
				if scenario.codex {
					assert.Equal(t, 1, strings.Count(response.Body.String(), `"type":"response.completed"`))
				}
				debits := ledger.recordedDebits()
				require.Len(t, debits, 1)
				assert.Negative(t, debits[0].DeltaUsdMicros, "the paid winner must not inherit the failed subscription")
				price, ok := catalog.PriceFor(providers.ProviderOpenAI, "gpt-5.6-luna")
				require.True(t, ok)
				assert.Equal(t, catalog.USDToMicros(catalog.EffectiveInputCost(8, 0, 0, price, providers.ProviderOpenAI)+catalog.EffectiveOutputCost(8, 3, price)), debits[0].NotionalCostMicros)
				store.mu.Lock()
				defer store.mu.Unlock()
				require.NotEmpty(t, store.usages)
				assert.Equal(t, "gpt-5.6-luna", store.usages[len(store.usages)-1].ServedModel)
			} else {
				require.Error(t, err)
				store.mu.Lock()
				defer store.mu.Unlock()
				assert.Empty(t, store.upserts)
				assert.Empty(t, store.usages)
			}
			if scenario.secondaryFails {
				assert.Equal(t, 1, strings.Count(response.Body.String(), "synthetic terminal failure"), response.Body.String())
			}
		})
	}
}
