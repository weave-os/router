package proxy

import (
	"context"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/router"
)

func TestVerificationUnprovenAnthropicExtraUsageNeverDispatched(t *testing.T) {
	for _, withAPI := range []bool{true, false} {
		t.Run(map[bool]string{true: "authorized-api", false: "no-api"}[withAPI], func(t *testing.T) {
			var oauthCharges, apiRequests int
			var winningPayload []byte
			var capturesMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturesMu.Lock()
				defer capturesMu.Unlock()
				if r.Header.Get("Authorization") != "" {
					oauthCharges++
				} else if r.Header.Get("X-Api-Key") == "synthetic-api-key" {
					apiRequests++
				} else {
					w.WriteHeader(403)
					return
				}
				winningPayload, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-4-8\",\"content\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"authorized Anthropic answer\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()
			apiKey := ""
			keyed := map[string]struct{}{}
			if withAPI {
				apiKey = "synthetic-api-key"
				keyed[providers.ProviderAnthropic] = struct{}{}
			}
			client := anthropic.NewClient(apiKey, server.URL)
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test"}}, map[string]providers.Client{providers.ProviderAnthropic: client}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-4-8", nil).WithDeploymentKeyedProviders(keyed)
			ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, "sk-ant-oat01-synthetic-unsafe-token")
			ctx = WithManagedSubscriptionUsage(ctx)
			body := `{"model":"auto","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"synthetic billing"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`
			rec := httptest.NewRecorder()
			err := svc.ProxyMessages(ctx, []byte(body), rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
			require.Zero(t, oauthCharges, "provider charges first unsafe OAuth request; prevention must precede dispatch")
			if withAPI {
				require.NoError(t, err)
				require.Equal(t, 1, apiRequests)
				require.Contains(t, rec.Body.String(), "authorized Anthropic answer")
				require.Equal(t, "read_file", gjson.GetBytes(winningPayload, "tools.0.name").String())
				require.False(t, servedOnSubscription(ctx))
			} else {
				require.Error(t, err)
				require.Zero(t, apiRequests)
			}
		})
	}
}

func TestVerificationSuppressedInboundAnthropicOAuthNeverRelayed(t *testing.T) {
	var oauthCharges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "sk-ant-oat") {
			oauthCharges.Add(1)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := anthropic.NewClient("", server.URL)
	svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test"}}, map[string]providers.Client{providers.ProviderAnthropic: client}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-4-8", nil).WithDeploymentKeyedProviders(map[string]struct{}{})
	ctx := context.WithValue(WithManagedSubscriptionUsage(context.Background()), InstallationSubscriptionRoutingDisabledContextKey{}, true)
	body := `{"model":"auto","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"synthetic billing"}]}`
	request := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer sk-ant-oat01-synthetic-inbound-token")
	_ = svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), request)
	require.Zero(t, oauthCharges.Load(), "a suppressed inbound subscription bearer must not be relayed by inference dispatch")
	passthroughRequest := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(body))
	passthroughRequest.Header.Set("Authorization", "Bearer sk-ant-oat01-synthetic-inbound-token")
	require.Error(t, svc.PassthroughToNamedProvider(ctx, providers.ProviderAnthropic, []byte(body), httptest.NewRecorder(), passthroughRequest))
	require.Zero(t, oauthCharges.Load(), "a suppressed inbound subscription bearer must not be relayed by the adapter passthrough tier")
}
