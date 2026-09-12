package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

func TestContentCapture_LeavesInferenceBytesUnchangedWithBlockedCollector(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		model    string
		body     string
		response string
		invoke   func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{
			"messages", providers.ProviderAnthropic, "claude-haiku-4-5",
			`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`,
			`{"id":"msg_test","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":1}}`,
			(*proxy.Service).ProxyMessages,
		},
		{
			"chat", providers.ProviderOpenAI, "gpt-4o",
			`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stop":["STOP"]}`,
			`{"id":"chat_test","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`,
			(*proxy.Service).ProxyOpenAIChatCompletion,
		},
		{
			"responses", providers.ProviderOpenAI, "gpt-5.5",
			`{"model":"gpt-5.5","input":"hello"}`,
			`{"id":"resp_test","object":"response","model":"gpt-5.5","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":10,"output_tokens":1}}`,
			(*proxy.Service).ProxyOpenAIResponses,
		},
		{
			"gemini", providers.ProviderGoogle, "gemini-2.5-pro",
			`{"model":"gemini-2.5-pro","contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1}}`,
			(*proxy.Service).ProxyGeminiGenerateContent,
		},
		{
			"messages-stream", providers.ProviderAnthropic, "claude-haiku-4-5",
			`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream":true}`,
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-4-5\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			(*proxy.Service).ProxyMessages,
		},
		{
			"chat-stream", providers.ProviderOpenAI, "gpt-4o",
			`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stop":["STOP"],"stream":true}`,
			"data: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\n" +
				"data: [DONE]\n\n",
			(*proxy.Service).ProxyOpenAIChatCompletion,
		},
		{
			"responses-stream", providers.ProviderOpenAI, "gpt-5.5",
			`{"model":"gpt-5.5","input":"hello","stream":true}`,
			"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"model\":\"gpt-5.5\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_test\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n",
			(*proxy.Service).ProxyOpenAIResponses,
		},
		{
			"gemini-stream", providers.ProviderGoogle, "gemini-2.5-pro",
			`{"model":"gemini-2.5-pro","contents":[{"role":"user","parts":[{"text":"hello"}]}],"stream":true}`,
			"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":1}}\n\n",
			(*proxy.Service).ProxyGeminiGenerateContent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			started := make(chan struct{}, 2)
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			defer collector.Close()
			defer close(release)
			em, err := otel.NewEmitter(otel.EmitterConfig{Endpoint: collector.URL, Workers: 1, QueueSize: 2, BatchSize: 1, ExportTimeout: time.Minute})
			require.NoError(t, err)
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				_ = em.Shutdown(ctx)
			}()
			var upstream, client []byte
			for _, mode := range []proxy.ContentCaptureMode{proxy.CaptureOff, proxy.CaptureFull, proxy.CaptureHashed} {
				provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
					if strings.HasSuffix(tc.name, "-stream") {
						w.Header().Set("Content-Type", "text/event-stream")
					} else {
						w.Header().Set("Content-Type", "application/json")
					}
					_, _ = io.WriteString(w, tc.response)
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}}
				svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: tc.provider, Model: tc.model}},
					map[string]providers.Client{tc.provider: provider}, em, false, nil, nil, false, tc.provider, tc.model, nil).
					WithContentCapture(mode, 4, nil)
				body := []byte(tc.body)
				recorder := httptest.NewRecorder()
				ctx := context.WithValue(context.Background(), proxy.InstallationHideTerminalSurfacesContextKey{}, true)
				ctx = em.NewBuffer().WithContext(ctx)
				req := httptest.NewRequest(http.MethodPost, "/v1/"+tc.name, strings.NewReader(tc.body))
				finished := make(chan error, 1)
				go func() { finished <- tc.invoke(svc, ctx, body, recorder, req) }()
				select {
				case err := <-finished:
					require.NoError(t, err)
				case <-time.After(3 * time.Second):
					t.Fatal("inference waited for the collector")
				}
				require.Len(t, provider.proxyBodies, 1)
				assert.Equal(t, tc.body, string(body))
				if mode == proxy.CaptureOff {
					upstream = provider.proxyBodies[0]
					client = recorder.Body.Bytes()
					require.NotEmpty(t, client)
					// Hold at least the upstream span export before captured calls run.
					select {
					case <-started:
					case <-time.After(3 * time.Second):
						t.Fatal("collector not reached")
					}
				} else {
					assert.Equal(t, upstream, provider.proxyBodies[0])
					assert.Equal(t, string(client), recorder.Body.String())
				}
			}
		})
	}
}
