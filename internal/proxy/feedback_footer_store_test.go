package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const footerStoreTestStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-4-5\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func footerStoreTestResponse(t *testing.T, withStore bool) string {
	t.Helper()
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5"},
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeProvider{
				proxyResponse: func(w http.ResponseWriter) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(footerStoreTestStream))
				},
			},
		},
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	if withStore {
		svc = svc.WithRouterFeedbackStore(&fakeFeedbackStore{})
	}
	ctx := requestcontext.WithClientIdentity(context.Background(), proxy.ClientIdentity{
		ClientApp: proxy.ClientAppClaudeCode,
		UserAgent: "claude-cli/2.1.302 (external, cli)",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-haiku-4-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyMessages(ctx, body, rec, req))
	return rec.Body.String()
}

func TestProxyMessages_FeedbackFooterFollowsRouterFeedbackStore(t *testing.T) {
	assert.Contains(t, footerStoreTestResponse(t, true), "Weave Router feedback:", "a durable store advertises /rf")
	assert.NotContains(t, footerStoreTestResponse(t, false), "Weave Router feedback:", "no store means no /rf hint")
}
