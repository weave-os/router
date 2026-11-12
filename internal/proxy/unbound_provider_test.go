package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A deployment-funded decision naming a provider the catalog does not bind for
// the model must never reach that provider: the plan fails closed with a typed
// reason, and only a surface's own rescue may serve an eligible binding.
func TestProxy_UnboundProviderDecisionFailsClosed(t *testing.T) {
	const model = "claude-haiku-4-5"
	_, bound := catalog.BindingFor(model, providers.ProviderFireworks)
	require.False(t, bound, "fixture needs a provider the catalog does not bind for the model")

	cases := map[string]struct {
		serve              func(*proxy.Service, http.ResponseWriter) error
		wantErrKind        proxy.DispatchErrorKind
		wantAnthropicCalls int32
	}{
		"messages rescues on the baseline binding": {
			serve: func(svc *proxy.Service, w http.ResponseWriter) error {
				body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":4096,"messages":[{"role":"user","content":"Say hi"}]}`)
				return svc.ProxyMessages(context.Background(), body, w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")))
			},
			wantAnthropicCalls: 1,
		},
		"chat fails closed": {
			serve: func(svc *proxy.Service, w http.ResponseWriter) error {
				body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
				return svc.ProxyOpenAIChatCompletion(context.Background(), body, w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("")))
			},
			wantErrKind: proxy.DispatchErrorPlanUnresolvable,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var unboundCalls, anthropicCalls atomic.Int32
			unbound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				unboundCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"served"},"finish_reason":"stop"}]}`))
			}))
			defer unbound.Close()
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				anthropicCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"served"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer native.Close()

			svc := proxy.NewService(
				&fakeRouter{decision: router.Decision{Provider: providers.ProviderFireworks, Model: model}},
				map[string]providers.Client{
					providers.ProviderFireworks: openaicompat.NewClient("test-fw-key", unbound.URL),
					providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", native.URL),
				},
				nil, false, nil, nil, false, providers.ProviderAnthropic, model, nil,
			).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderFireworks: {}, providers.ProviderAnthropic: {}})

			err := tc.serve(svc, httptest.NewRecorder())

			assert.Zero(t, unboundCalls.Load(), "unbound provider must never be dispatched to")
			assert.Equal(t, tc.wantAnthropicCalls, anthropicCalls.Load())
			if tc.wantErrKind == proxy.DispatchErrorNone {
				require.NoError(t, err)
				return
			}
			classification, ok := proxy.ClassifyDispatchError(err)
			require.True(t, ok, "fail-closed error must be typed: %v", err)
			assert.Equal(t, tc.wantErrKind, classification.Kind)
		})
	}
}
