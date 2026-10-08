package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

func TestProxyMessages_AnthropicToolAliasRoundTrip(t *testing.T) {
	name := strings.Repeat("x", 65)
	const alias = "invalid_tool_78c741ddc482e4cdf8c474a0876347a0905b6233"
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			upstream := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				response := fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"call_next","name":%q,"input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":20}}`, alias)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"usage\":{\"input_tokens\":100}}}\n\n" +
						fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_next\",\"name\":%q,\"input\":{}}}\n\n", alias) +
						"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":20}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(http.StatusOK)
				for _, b := range []byte(response) {
					_, _ = w.Write([]byte{b})
				}
			}}
			decision := router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-5-5"}
			svc := proxy.NewService(&fakeRouter{decision: decision}, map[string]providers.Client{providers.ProviderAnthropic: upstream}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-sonnet-5-5", nil)
			body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-5-5","stream":%t,"max_tokens":1024,"tools":[{"name":"ToolSearch","input_schema":{"type":"object"}},{"name":%q,"defer_loading":true,"input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"find a tool"},{"role":"assistant","content":[{"type":"tool_use","id":"search","name":"ToolSearch","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"search","content":[{"type":"tool_reference","name":%q}]}]}]}`, stream, name, name))
			sink := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			require.NoError(t, svc.ProxyMessages(context.Background(), body, sink, req))
			require.Len(t, upstream.proxyBodies, 1)
			require.Equal(t, alias, gjson.GetBytes(upstream.proxyBodies[0], "tools.1.name").String())
			require.Equal(t, alias, gjson.GetBytes(upstream.proxyBodies[0], "messages.2.content.0.content.0.name").String())
			if stream {
				foundToolUse := false
				for _, line := range strings.Split(sink.Body.String(), "\n") {
					if strings.HasPrefix(line, "data: ") && gjson.Get(line[6:], "content_block.type").String() == "tool_use" {
						foundToolUse = true
						require.Equal(t, name, gjson.Get(line[6:], "content_block.name").String())
					}
				}
				require.True(t, foundToolUse, "stream must contain a tool_use event")
			} else {
				require.Equal(t, name, gjson.Get(sink.Body.String(), "content.0.name").String())
			}
			require.NotContains(t, sink.Body.String(), alias)
			require.Equal(t, "nosniff", sink.Header().Get("X-Content-Type-Options"))
			require.Contains(t, sink.Body.String(), "call_next")
		})
	}
}
