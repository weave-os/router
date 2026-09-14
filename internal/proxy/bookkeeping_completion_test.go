package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

type completionPinStore struct {
	*fakePinStore
	started chan struct{}
	release chan struct{}
}

func (s *completionPinStore) UpdateUsage(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string, usage sessionpin.Usage) error {
	close(s.started)
	// Deliberately ignore the deadline to prove response delivery is independent
	// of this write, not merely released after the new write timeout.
	<-s.release
	return s.fakePinStore.UpdateUsage(ctx, key, role, usage)
}

func TestBufferedInferenceCompletesBeforeUsagePersistence(t *testing.T) {
	for _, tt := range []struct {
		name, path, body, provider, model, upstream string
		call                                        func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{
			name: "messages", path: "/v1/messages", provider: providers.ProviderAnthropic, model: "claude-opus-4-7",
			body:     pinTestBody,
			upstream: `{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"complete answer"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":9}}`,
			call:     (*proxy.Service).ProxyMessages,
		},
		{
			name: "chat", path: "/v1/chat/completions", provider: providers.ProviderOpenAI, model: "gpt-5.5",
			body:     `{"model":"gpt-5.5","messages":[{"role":"user","content":"Write a small program to sort a list"}]}`,
			upstream: `{"id":"chat_test","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"complete answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":9}}`,
			call:     (*proxy.Service).ProxyOpenAIChatCompletion,
		},
		{
			name: "native responses", path: "/v1/responses", provider: providers.ProviderOpenAI, model: "gpt-5.5",
			body:     `{"model":"gpt-5.5","input":"Write a small program to sort a list","tools":[{"type":"custom","name":"apply_patch"}]}`,
			upstream: `{"id":"resp_test","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"complete answer"}]}],"usage":{"input_tokens":12,"output_tokens":9}}`,
			call:     (*proxy.Service).ProxyOpenAIResponses,
		},
		{
			name: "gemini", path: "/v1beta/models/gemini-2.5-pro:generateContent", provider: providers.ProviderGoogle, model: "gemini-2.5-pro",
			body:     `{"model":"gemini-2.5-pro","contents":[{"role":"user","parts":[{"text":"Write a small program to sort a list"}]}]}`,
			upstream: `{"candidates":[{"content":{"role":"model","parts":[{"text":"complete answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":9,"totalTokenCount":21}}`,
			call:     (*proxy.Service).ProxyGeminiGenerateContent,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &completionPinStore{fakePinStore: newFakePinStore(), started: make(chan struct{}), release: make(chan struct{})}
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.upstream)
			}}
			service := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: tt.provider, Model: tt.model, Reason: "test"}}, map[string]providers.Client{tt.provider: provider}, nil, false, nil, store, false, tt.provider, tt.model, recordingTelemetry{}).WithOpenAIResponsesBroad(false)
			handlerDone := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerDone <- tt.call(service, authedCtx("00000000-0000-0000-0000-000000000001"), []byte(tt.body), w, r)
			}))
			defer server.Close()
			defer close(store.release)
			client := &http.Client{Timeout: 2 * time.Second}
			response, err := client.Post(server.URL+tt.path, "application/json", strings.NewReader(tt.body))
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err, "response must end while bookkeeping is blocked")
			assert.Equal(t, http.StatusOK, response.StatusCode)
			assert.Contains(t, string(body), "complete answer")
			assert.True(t, json.Valid(body), "finalization must not duplicate the JSON response")
			assert.Equal(t, tt.model, response.Header.Get(proxy.HeaderRouterModel))
			select {
			case <-store.started:
			case err := <-handlerDone:
				t.Fatalf("test did not reach usage persistence: %v", err)
			case <-time.After(time.Second):
				t.Fatal("usage persistence never started")
			}
			select {
			case err := <-handlerDone:
				t.Fatalf("bookkeeping was not blocked: %v", err)
			default:
			}
		})
	}
}
