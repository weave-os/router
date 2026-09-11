package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

func TestDependencyFallbackPreservesEveryNativeProtocol(t *testing.T) {
	for _, tt := range []struct {
		name, model, provider, path, request, response, streamFixture string
		client                                                        func(string) providers.Client
		call                                                          func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{
			name: "messages", model: "claude-opus-4-8", provider: providers.ProviderAnthropic, path: "/v1/messages",
			request:       `{"model":"claude-opus-4-8","max_tokens":4096,"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"messages":[{"role":"user","content":"Keep this entire original history."}],"future_native_field":{"preserve":true}}`,
			response:      `{"id":"msg_original","type":"message","role":"assistant","content":[{"type":"text","text":"original answer"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":9}}`,
			streamFixture: "anthropic/basic_text.upstream.sse", client: anthropicClient, call: (*proxy.Service).ProxyMessages,
		},
		{
			name: "chat", model: "gpt-5.5", provider: providers.ProviderOpenAI, path: "/v1/chat/completions",
			request:       `{"model":"gpt-5.5","messages":[{"role":"user","content":"Keep this entire original history."}],"parallel_tool_calls":false,"future_native_field":{"preserve":true}}`,
			response:      `{"id":"chat_original","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"original answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":9}}`,
			streamFixture: "openai_chat/basic_text.upstream.sse", client: openAIClient, call: (*proxy.Service).ProxyOpenAIChatCompletion,
		},
		{
			name: "responses", model: "gpt-5.5", provider: providers.ProviderOpenAI, path: "/v1/responses",
			request:       `{"model":"gpt-5.5","input":"Keep this entire original history.","previous_response_id":"resp_prior","store":false,"future_native_field":{"preserve":true}}`,
			response:      `{"id":"resp_original","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"original answer"}]}],"usage":{"input_tokens":12,"output_tokens":9}}`,
			streamFixture: "responses/toolcall.upstream.sse", client: openAIClient, call: (*proxy.Service).ProxyOpenAIResponses,
		},
		{
			name: "gemini", model: "gemini-2.5-pro", provider: providers.ProviderGoogle, path: "/v1beta/models/gemini-2.5-pro:generateContent",
			request:       `{"contents":[{"role":"user","parts":[{"text":"Keep this entire original history.","thoughtSignature":"opaque-signature"}]}],"future_native_field":{"preserve":true}}`,
			response:      `{"candidates":[{"content":{"role":"model","parts":[{"text":"original answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":9}}`,
			streamFixture: "gemini_native/basic_text.upstream.sse", client: geminiClient, call: (*proxy.Service).ProxyGeminiGenerateContent,
		},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tt.name, streaming), func(t *testing.T) {
				response := []byte(tt.response)
				if streaming {
					response = readFixture(t, tt.streamFixture)
				}
				upstream, server := newMockUpstream(t, streaming, http.StatusOK, response)
				failedPolicy := &fakeRouter{err: fmt.Errorf("policy deadline: %w", context.DeadlineExceeded)}
				service := proxy.NewService(failedPolicy, map[string]providers.Client{tt.provider: tt.client(server.URL)}, nil, false, nil, nil, false, tt.provider, tt.model, nil).
					WithDeploymentKeyedProviders(map[string]struct{}{tt.provider: {}}).
					WithPolicyDeadlineFallback(true).
					WithOpenAIResponsesBroad(false).
					WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
				body := []byte(tt.request)
				path := tt.path
				ctx := authedCtx("00000000-0000-0000-0000-000000000001")
				if tt.provider == providers.ProviderGoogle {
					ctx = proxy.WithOriginalGeminiBody(ctx, body)
					if streaming {
						path = strings.Replace(path, ":generateContent", ":streamGenerateContent?alt=sse", 1)
					}
					var err error
					body, err = sjson.SetBytes(body, "model", tt.model)
					require.NoError(t, err)
				} else {
					var err error
					body, err = sjson.SetBytes(body, "stream", streaming)
					require.NoError(t, err)
				}
				wantBody := string(body)
				if tt.provider == providers.ProviderGoogle {
					wantBody = tt.request
					var err error
					body, err = sjson.SetBytes(body, "stream", streaming)
					require.NoError(t, err)
				}
				request := httptest.NewRequest(http.MethodPost, path, nil)
				request.Header.Set("X-Weave-Router-Key", "rk_never-forward")
				request.Header.Set("Authorization", "Bearer rk_never-forward")
				request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")
				recorder := httptest.NewRecorder()
				require.NoError(t, tt.call(service, ctx, body, recorder, request))
				gotPath, gotBody, gotHeaders := upstream.captured(t)
				assert.Equal(t, strings.Split(path, "?")[0], gotPath)
				assert.Equal(t, wantBody, string(gotBody))
				if streaming {
					assert.Equal(t, string(normalizeSSE(t, response)), string(normalizeSSE(t, recorder.Body.Bytes())), "the native stream reaches the caller with only router cost metadata added")
				} else {
					assert.Equal(t, string(response), recorder.Body.String())
				}
				assert.Equal(t, tt.model, recorder.Header().Get(proxy.HeaderRouterModel))
				assert.Equal(t, tt.provider, recorder.Header().Get(proxy.HeaderRouterProvider))
				assert.Equal(t, "policy_unavailable", recorder.Header().Get(proxy.HeaderRouterFailOpenReason))
				assert.Empty(t, gotHeaders.Get("X-Weave-Router-Key"))
				assert.NotContains(t, fmt.Sprint(gotHeaders), "rk_never-forward")
				if tt.provider == providers.ProviderAnthropic {
					assert.Equal(t, "interleaved-thinking-2025-05-14", gotHeaders.Get("Anthropic-Beta"))
				}
			})
		}
	}
}

func TestDependencyFallbackRemainsDisabledByDefault(t *testing.T) {
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) { _, _ = w.Write([]byte("unexpected")) }}
	failure := errors.New("policy unavailable")
	service := proxy.NewService(&fakeRouter{err: failure}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-4-8", nil)
	rec := httptest.NewRecorder()
	err := service.ProxyMessages(context.Background(), []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	assert.ErrorIs(t, err, failure)
	assert.Empty(t, provider.proxyBodies)
	assert.Empty(t, rec.Body.String())
}

type cancelledPolicy struct{}

func (cancelledPolicy) Route(ctx context.Context, _ router.Request) (router.Decision, error) {
	<-ctx.Done()
	return router.Decision{}, ctx.Err()
}

func TestDependencyFallbackDoesNotOutliveCaller(t *testing.T) {
	provider := &fakeProvider{}
	service := proxy.NewService(cancelledPolicy{}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-4-8", nil).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := service.ProxyMessages(ctx, []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, provider.proxyBodies)
}

func TestDependencyFallbackUpstreamFailureIsTerminal(t *testing.T) {
	provider := &fakeProvider{proxyErr: &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}}
	service := proxy.NewService(&fakeRouter{err: errors.New("policy unavailable")}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-4-8", nil).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), requestcontext.DefaultPreparationLimits())
	err := service.ProxyMessages(context.Background(), []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	var upstream *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.Status)
	require.Len(t, provider.proxyBodies, 1)
}

func TestDependencyFallbackLeavesTimeForOriginalProvider(t *testing.T) {
	provider := &liveContextProvider{fakeProvider: fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"content":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}}}
	limits := requestcontext.DefaultPreparationLimits()
	limits.Total = 10 * time.Millisecond
	service := proxy.NewService(cancelledPolicy{}, map[string]providers.Client{providers.ProviderAnthropic: provider}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-4-8", nil).
		WithDependencyFailOpen(requestcontext.NewDependencyHealth(), limits)
	recorder := httptest.NewRecorder()
	require.NoError(t, service.ProxyMessages(context.Background(), []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`), recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	assert.JSONEq(t, `{"content":[],"usage":{"input_tokens":1,"output_tokens":1}}`, recorder.Body.String())
	require.Len(t, provider.proxyBodies, 1)
}

type liveContextProvider struct{ fakeProvider }

func (p *liveContextProvider) Proxy(ctx context.Context, decision router.Decision, prepared providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.fakeProvider.Proxy(ctx, decision, prepared, w, r)
}
