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
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const fallbackToolModel = "gpt-5.6-luna"

func TestProxyMessages_OpenAIResponses404UsesValidChatToolFallback(t *testing.T) {
	for _, broad := range []bool{false, true} {
		name := "narrow"
		if broad {
			name = "broad"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			var efforts []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if !assert.NoError(t, err) {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/v1/responses" {
					efforts = append(efforts, gjson.GetBytes(body, "reasoning.effort").String())
					w.WriteHeader(http.StatusNotFound)
					return
				}
				effort := gjson.GetBytes(body, "reasoning_effort").String()
				efforts = append(efforts, effort)
				if effort != "none" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"Function tools with reasoning_effort are not supported. Use /v1/responses or set reasoning_effort to 'none'."}}`)
					return
				}
				assert.Equal(t, "read_file", gjson.GetBytes(body, "tools.0.function.name").String())
				writeOpenAIChatSSE(w)
			}))
			defer upstream.Close()

			svc := proxy.NewService(
				&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: fallbackToolModel}},
				map[string]providers.Client{providers.ProviderOpenAI: openai.NewClient("test-key", upstream.URL)},
				nil, false, nil, nil, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil,
			).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}}).
				WithOpenAIResponsesBroad(broad)
			body := []byte(`{"model":"auto","stream":true,"max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"list files"}],"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
			for i := 0; i < 2; i++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
				require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))
				assert.Equal(t, http.StatusOK, rec.Code)
				assert.Contains(t, rec.Body.String(), `"text":"hi"`)
				assert.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop\n"))
				assert.NotContains(t, rec.Body.String(), "event: error")
			}
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, []string{"/v1/responses", "/v1/chat/completions", "/v1/responses", "/v1/chat/completions"}, paths)
			assert.Equal(t, []string{"low", "none", "low", "none"}, efforts)
		})
	}
}
