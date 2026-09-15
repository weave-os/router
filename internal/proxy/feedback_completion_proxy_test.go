package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/feedback"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cache"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type historyCompletionStore struct {
	records      []proxy.FeedbackRequest
	beforeCommit func() error
}

func (s *historyCompletionStore) CompleteFeedbackRequest(_ context.Context, r proxy.FeedbackRequest) error {
	if s.beforeCommit != nil {
		if err := s.beforeCommit(); err != nil {
			return err
		}
	}
	s.records = append(s.records, r)
	return nil
}
func (*historyCompletionStore) AcceptRouterFeedback(_ context.Context, event proxy.RouterFeedbackEvent) (proxy.RouterFeedbackEvent, error) {
	return event, nil
}

const (
	completionBaselineModel   = "claude-opus-4-8"
	completionOSSModel        = "deepseek/deepseek-v4-pro"
	completionAnthropicModel  = "claude-haiku-4-5"
	completionOpenAIModel     = "gpt-5.5"
	completionGeminiModel     = "gemini-2.5-pro"
	completionAnthropicBody   = `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0}}`
	completionAnthropicStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
)

var completionSurfaces = []struct {
	name, provider, model, request, body, stream, terminal string
	call                                                   func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
}{
	{"anthropic", providers.ProviderAnthropic, completionAnthropicModel, `{"model":"` + bypassRequestedMdl + `","max_tokens":4096,"messages":[{"role":"user","content":"hello"}]}`, completionAnthropicBody, completionAnthropicStream, `"stop_reason":"end_turn"`, (*proxy.Service).ProxyMessages},
	{"chat", providers.ProviderOpenAI, completionOpenAIModel, `{"model":"` + completionOpenAIModel + `","messages":[{"role":"user","content":"hello"}]}`, `{"id":"chat_test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, "data: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", `"finish_reason":"stop"`, (*proxy.Service).ProxyOpenAIChatCompletion},
	{"native_responses", providers.ProviderOpenAI, completionOpenAIModel, `{"model":"` + completionOpenAIModel + `","input":"hello","tools":[{"type":"custom","name":"apply_patch"}]}`, `{"id":"resp_test","object":"response","status":"completed","output":[]}`, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"status\":\"in_progress\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[]}}\n\n", `"status":"completed"`, (*proxy.Service).ProxyOpenAIResponses},
	{"translated_responses", providers.ProviderAnthropic, completionAnthropicModel, `{"model":"` + completionAnthropicModel + `","input":"hello"}`, completionAnthropicBody, completionAnthropicStream, `"type":"response.completed"`, (*proxy.Service).ProxyOpenAIResponses},
	{"gemini", providers.ProviderGoogle, completionGeminiModel, `{"model":"` + completionGeminiModel + `","contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, `{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`, "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\ndata: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"}]}\n\n", `"finishReason":"STOP"`, (*proxy.Service).ProxyGeminiGenerateContent},
}

func TestFeedbackHistoryAcrossClientSurfaces(t *testing.T) {
	for _, tc := range completionSurfaces {
		for _, stream := range []bool{false, true} {
			for _, fail := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%v/fail=%v", tc.name, stream, fail), func(t *testing.T) {
					rec := httptest.NewRecorder()
					store := &historyCompletionStore{beforeCommit: func() error {
						if stream {
							require.NotContains(t, rec.Body.String(), tc.terminal)
						} else {
							require.Empty(t, rec.Body.String())
						}
						if fail {
							return errors.New("history unavailable")
						}
						return nil
					}}
					provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
						body := tc.body
						ct := "application/json"
						if stream {
							body = tc.stream
							ct = "text/event-stream"
						}
						w.Header().Set("Content-Type", ct)
						w.WriteHeader(http.StatusOK)
						for offset := 0; offset < len(body); offset += 11 {
							_, _ = w.Write([]byte(body[offset:min(offset+11, len(body))]))
						}
						if f, ok := w.(http.Flusher); ok {
							f.Flush()
						}
					}}
					svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: tc.provider, Model: tc.model}}, map[string]providers.Client{tc.provider: provider}, nil, false, nil, nil, false, tc.provider, tc.model, nil).WithRouterFeedbackStore(store).WithOpenAIResponsesBroad(false)
					body := tc.request
					if stream {
						body = strings.TrimSuffix(body, "}") + `,"stream":true}`
					}
					req := httptest.NewRequest(http.MethodPost, "/test", nil)
					req.Header.Set(routingMarkerHeader, "off")
					err := tc.call(svc, authedCtx(uuid.NewString()), []byte(body), rec, req)
					if fail {
						require.ErrorContains(t, err, "history unavailable")
						require.Empty(t, store.records)
						if !stream {
							require.Empty(t, rec.Body.String())
						}
					} else {
						require.NoError(t, err)
						require.Len(t, store.records, 1)
						require.Equal(t, tc.model, store.records[0].ServedModel)
						require.Equal(t, tc.provider, store.records[0].ServedProvider)
						require.Len(t, store.records[0].SessionKey, 16)
						require.NotEmpty(t, rec.Body.String())
					}
					require.Len(t, provider.proxyBodies, 1, "history failure must not repeat inference")
				})
			}
		}
	}
}

func TestFeedbackHistoryLiveUsageBypass(t *testing.T) {
	for _, stream := range []bool{false, true} {
		svc, fr, p := bypassFixture(t, 0.2)
		rec, req, body := bypassRequest(t)
		store := &historyCompletionStore{beforeCommit: func() error { require.NotContains(t, rec.Body.String(), `"stop_reason":"end_turn"`); return nil }}
		svc.WithRouterFeedbackStore(store)
		if stream {
			body = []byte(strings.TrimSuffix(string(body), "}") + `,"stream":true}`)
			p.proxyResponse = func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(completionAnthropicStream))
			}
		}
		ctx := context.WithValue(bypassCtx(.8), proxy.InstallationIDContextKey{}, uuid.NewString())
		ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "bypass-history-key")
		require.NoError(t, svc.ProxyMessages(ctx, body, rec, req))
		require.Len(t, store.records, 1)
		require.Equal(t, bypassRequestedMdl, store.records[0].ServedModel)
		require.Empty(t, store.records[0].RouteID)
		require.Zero(t, fr.routeCalls)
	}
}

func TestFeedbackHistoryCacheReplayUsesProducerAndFreshRequest(t *testing.T) {
	store := &historyCompletionStore{}
	semantic := cache.New(cache.DefaultConfig())
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: completionAnthropicModel, Metadata: &router.RoutingMetadata{Embedding: []float32{1, 0}, ClusterIDs: []int{1}}}}
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completionAnthropicBody))
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, semantic, nil, false, providers.ProviderAnthropic, completionAnthropicModel, nil).WithRouterFeedbackStore(store).WithFeedback(nil, feedback.NewSigner("fixture-signing-secret", 0), "https://feedback.example.test")
	ctx := context.WithValue(authedCtx(uuid.NewString()), proxy.ExternalIDContextKey{}, "cache-history-fixture")
	body := []byte(`{"model":"` + completionAnthropicModel + `","messages":[{"role":"user","content":"hello"}]}`)
	first := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, first, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	fr.decision.Model = bypassRequestedMdl
	second := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, body, second, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	require.Len(t, provider.proxyBodies, 1)
	require.Len(t, store.records, 2)
	require.NotEqual(t, store.records[0].RequestID, store.records[1].RequestID)
	require.Equal(t, completionAnthropicModel, store.records[1].ServedModel)
	require.Empty(t, store.records[1].RouteID)
	require.Empty(t, store.records[1].Strategy)
	require.Equal(t, completionAnthropicModel, second.Header().Get(proxy.HeaderRouterModel))
	require.NotEqual(t, first.Header().Get(proxy.HeaderRouterFeedbackURL), second.Header().Get(proxy.HeaderRouterFeedbackURL))
	require.NotEmpty(t, second.Header().Get(proxy.HeaderRouterFeedbackURL))
	store.beforeCommit = func() error { return errors.New("cache history unavailable") }
	third := httptest.NewRecorder()
	require.ErrorContains(t, svc.ProxyMessages(ctx, body, third, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)), "cache history unavailable")
	require.Empty(t, third.Body.String())
	require.Len(t, provider.proxyBodies, 1)
	require.Len(t, store.records, 2)
}

func TestFeedbackHistoryFailedUsageBypassDoesNotReroute(t *testing.T) {
	svc, fr, p := bypassFixture(t, .2)
	rec, req, body := bypassRequest(t)
	store := &historyCompletionStore{beforeCommit: func() error { return errors.New("bypass history unavailable") }}
	svc.WithRouterFeedbackStore(store)
	ctx := context.WithValue(bypassCtx(.8), proxy.InstallationIDContextKey{}, uuid.NewString())
	ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "bypass-history-key")
	require.ErrorContains(t, svc.ProxyMessages(ctx, body, rec, req), "bypass history unavailable")
	require.Empty(t, store.records)
	require.Empty(t, rec.Body.String())
	require.Zero(t, fr.routeCalls)
	require.Len(t, p.proxyBodies, 1)
}

func TestFeedbackHistoryForcedToolResultKeepsRequestedTierScope(t *testing.T) {
	pins := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: completionAnthropicModel}}
	svc, p := newPinSvcCapturing(fr, pins)
	p.proxyResponse = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completionAnthropicBody))
	}
	history := &historyCompletionStore{}
	svc.WithRouterFeedbackStore(history)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-weave-force-model", completionAnthropicModel)
	err := svc.ProxyMessages(authedCtx(uuid.NewString()), []byte(toolResultPinnedBody), httptest.NewRecorder(), req)
	require.NoError(t, err)
	require.Len(t, history.records, 1)
	require.Equal(t, completionAnthropicModel, history.records[0].ServedModel)
	require.Equal(t, "default_high", history.records[0].Role)
}
