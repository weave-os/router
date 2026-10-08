package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

func TestProxyMessages_OpenAIToolAliasRoundTrip(t *testing.T) {
	const name = "mcp__claude_ai_Atlassian_Rovo_2__getJiraProjectIssueTypesMetadata"
	var upstream *fakeProvider
	var alias string
	upstream = &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		sent := upstream.proxyBodies[len(upstream.proxyBodies)-1]
		if alias = gjson.GetBytes(sent, "tools.0.function.name").String(); alias != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"id":"c","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_next","type":"function","function":{"name":%q,"arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, alias)
			return
		}
		alias = gjson.GetBytes(sent, "tools.0.name").String()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		item := fmt.Sprintf(`{"type":"function_call","id":"fc","call_id":"call_next","name":%q,"arguments":"{}","status":"completed"}`, alias)
		_, _ = fmt.Fprintf(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":%[1]s}\n\n"+
			"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%[1]s}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[%[1]s],\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n", item)
	}}
	decision := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.5"}
	svc := proxy.NewService(&fakeRouter{decision: decision}, map[string]providers.Client{providers.ProviderOpenAI: upstream}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.5", nil)
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-5-5","max_tokens":1024,"tools":[{"name":%q,"input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"list issue types"}]}`, name))
	sink := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(context.Background(), body, sink, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
	require.NotEqual(t, name, alias)
	require.LessOrEqual(t, len(alias), 64)
	require.Equal(t, name, gjson.Get(sink.Body.String(), `content.#(type=="tool_use").name`).String(), sink.Body.String())
	require.NotContains(t, sink.Body.String(), alias)
}
