package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/translate"
)

func TestGeminiToolModeRecoveryAcrossOpenAIFormats(t *testing.T) {
	const model = "gemini-3.1-pro-preview"
	for _, tc := range []struct {
		name   string
		format translate.Format
	}{
		{name: "chat", format: translate.FormatOpenAI},
		{name: "responses", format: translate.FormatOpenAIResponses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &sequencedClient{responses: []func(http.ResponseWriter) error{
				func(http.ResponseWriter) error {
					return &providers.UpstreamStatusError{Status: http.StatusBadRequest}
				},
				func(w http.ResponseWriter) error {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					_, err := io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":"synthetic answer"}],"role":"model"},"index":0}]}`+"\n\n"+
						`data: {"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":4,"totalTokenCount":9}}`+"\n\n")
					return err
				},
			}}
			svc := proxy.WithStrategyPorts(proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: model}},
				dispatch.NewClients(map[string]providers.Client{providers.ProviderGoogle: client}), nil, nil, false, providers.ProviderGoogle, model, nil), hmm.ServingPorts()).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderGoogle: {}})
			rec := httptest.NewRecorder()
			body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic task"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`
			var err error
			if tc.format == translate.FormatOpenAIResponses {
				body = `{"model":"auto","stream":true,"input":"synthetic task","tools":[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`
				err = svc.ProxyOpenAIResponses(context.Background(), []byte(body), rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(body)))
			} else {
				err = svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
			}
			require.NoError(t, err)
			require.Len(t, client.bodies, 2)
			assert.Contains(t, string(client.bodies[0]), `"mode":"VALIDATED"`)
			assert.Contains(t, string(client.bodies[1]), `"mode":"AUTO"`)
			assert.Contains(t, rec.Body.String(), "synthetic answer")
			if tc.format == translate.FormatOpenAIResponses {
				assert.Contains(t, rec.Body.String(), `"type":"response.completed"`)
				assert.NotContains(t, rec.Body.String(), `"choices":`)
			} else {
				assert.Contains(t, rec.Body.String(), "data: [DONE]")
			}
		})
	}
}
