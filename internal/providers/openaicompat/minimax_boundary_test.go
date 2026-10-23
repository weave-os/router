package openaicompat_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

func TestMiniMaxProxyCatalogBoundary(t *testing.T) {
	ids := make(map[string]string)
	for _, model := range catalog.Models {
		for _, binding := range model.Providers {
			if binding.Provider == providers.ProviderMiniMax {
				ids[model.ID] = binding.UpstreamID
			}
		}
	}
	for _, tc := range []struct {
		name, model, upstream, effort string
	}{
		{"flash default", "minimax/minimax-m3.1-flash", "MiniMax-M3.1-Flash", ""},
		{"flash max", "minimax/minimax-m3.1-flash", "MiniMax-M3.1-Flash", "max"},
		{"flash xhigh", "minimax/minimax-m3.1-flash", "MiniMax-M3.1-Flash", "xhigh"},
		{"explicit M3", "minimax/minimax-m3", "MiniMax-M3", ""},
		{"saved legacy", "minimax/minimax-m2.7", "MiniMax-M2.7", ""},
		{"custom", "custom-model", "custom-model", ""},
		{"non MiniMax", "gpt-4o", "gpt-4o", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v1/chat/completions", r.URL.Path)
				assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"chatcmpl-test","choices":[]}`)
			}))
			t.Cleanup(upstream.Close)
			client := openaicompat.NewClientWithModelIDMap("test-key", upstream.URL+"/v1", ids)
			messages := []any{
				map[string]any{"role": "assistant", "content": "previous answer", "reasoning_content": "previous reasoning"},
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": "continue"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}},
				}},
			}
			body := map[string]any{"model": tc.model, "messages": messages, "max_tokens": 600000}
			if tc.effort != "" {
				body["reasoning_effort"] = tc.effort
			}
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			envelope, err := translate.ParseOpenAI(raw)
			require.NoError(t, err)
			prepared, err := envelope.PrepareOpenAI(http.Header{}, translate.EmitOptions{
				TargetModel: tc.model, Capabilities: router.Lookup(tc.model),
			})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			err = client.Proxy(context.Background(), router.Decision{Model: tc.model}, providers.PreparedRequest{
				Body: prepared.Body, Headers: prepared.Headers,
			}, response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, tc.upstream, got["model"])
			assert.Equal(t, messages, got["messages"])
			if tc.effort != "" {
				assert.Equal(t, tc.effort, got["reasoning_effort"])
			} else {
				assert.NotContains(t, got, "reasoning_effort")
			}
			if tc.model == "minimax/minimax-m3.1-flash" {
				assert.Equal(t, float64(524288), got["max_completion_tokens"])
				assert.NotContains(t, got, "service_tier")
				assert.NotContains(t, got, "thinking")
			}
		})
	}
}
