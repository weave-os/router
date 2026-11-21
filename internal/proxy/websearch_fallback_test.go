package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/websearch"
)

const fallbackSearchModel = "claude-sonnet-5"

func claudeCodeSearchHelperBody(stream bool, extraToolFieldsJSON string) []byte {
	streamValue := "false"
	if stream {
		streamValue = "true"
	}
	return []byte(`{"model":"` + fallbackSearchModel + `","stream":` + streamValue + `,"max_tokens":1024,` +
		`"system":"You are an assistant for performing a web search tool use",` +
		`"tools":[{"type":"web_search_20250305","name":"web_search"` + extraToolFieldsJSON + `}],` +
		`"messages":[{"role":"user","content":"Perform a web search for the query: router release notes"}]}`)
}

func nonAnthropicProviders() map[string]struct{} {
	return map[string]struct{}{
		providers.ProviderOpenAI: {}, providers.ProviderGoogle: {}, providers.ProviderOpenRouter: {},
	}
}

func fallbackSearchResult() websearch.Response {
	return websearch.Response{
		Summary: "Release notes list the changes.",
		Results: []websearch.Result{{Title: "Release notes", URL: "https://docs.example/releases", Snippet: "Changes"}},
	}
}

func serveTestMessagesWebSearch(t *testing.T, ctx context.Context, s *Service, body []byte, stream bool, enabled map[string]struct{}, w http.ResponseWriter) bool {
	t.Helper()
	turn := messagesWebSearchTurn{body: body, model: fallbackSearchModel, stream: stream, inputTokens: 12, enabled: enabled}
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, routePathMessages, nil)
	served, err := turn.serve(ctx, s, req, w)
	require.NoError(t, err)
	return served
}

func TestServeNativeWebSearchFallbackServesNonAnthropicDeployment(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			fallback := &fakeSearch{resp: fallbackSearchResult()}
			s := &Service{webSearch: webSearchExecutors{fallback: fallback}}
			rec := httptest.NewRecorder()

			served := serveTestMessagesWebSearch(t, t.Context(), s, claudeCodeSearchHelperBody(stream, ""), stream, nonAnthropicProviders(), rec)

			require.True(t, served, "the fallback executor must answer the search turn")
			assert.Equal(t, 1, fallback.runs)
			assert.Equal(t, "router release notes", fallback.got.Text)
			if stream {
				assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
				sseBody := rec.Body.String()
				for _, want := range []string{"event: message_start", `"type":"web_search_tool_result"`, "https://docs.example/releases", "event: message_stop"} {
					assert.Contains(t, sseBody, want)
				}
				return
			}
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			body := gjson.ParseBytes(rec.Body.Bytes())
			assert.Equal(t, "server_tool_use", body.Get("content.0.type").String())
			assert.Equal(t, "web_search_tool_result", body.Get("content.1.type").String())
			assert.Equal(t, "https://docs.example/releases", body.Get("content.1.content.0.url").String())
			assert.Equal(t, fallbackSearchModel, body.Get("model").String())
		})
	}
}

func TestServeNativeWebSearch_AllowedDomainsBypassesFallback(t *testing.T) {
	for name, extra := range map[string]string{
		"allowed_domains": `,"allowed_domains":["docs.example"]`,
		"blocked_domains": `,"blocked_domains":["spam.example"]`,
	} {
		t.Run(name, func(t *testing.T) {
			fallback := &fakeSearch{resp: fallbackSearchResult()}
			s := &Service{webSearch: webSearchExecutors{fallback: fallback}}
			rec := httptest.NewRecorder()

			served := serveTestMessagesWebSearch(t, t.Context(), s, claudeCodeSearchHelperBody(false, extra), false, nonAnthropicProviders(), rec)

			assert.False(t, served, "a domain-filtered search must stay on normal routing")
			assert.Zero(t, fallback.runs, "the fallback must not widen a domain-filtered search")
			assert.Zero(t, rec.Body.Len())
		})
	}
}

func TestServeNativeWebSearchGatewayCredentialUsesCortexOnly(t *testing.T) {
	cortex := &fakeSearch{resp: websearch.Response{Results: []websearch.Result{{Title: "Cortex", URL: "https://docs.example/cortex"}}}}
	fallback := &fakeSearch{resp: fallbackSearchResult()}
	s := &Service{webSearch: webSearchExecutors{gateway: cortex, fallback: fallback}}
	rec := httptest.NewRecorder()

	require.True(t, serveTestMessagesWebSearch(t, gatewayCtx(), s, claudeCodeSearchHelperBody(false, ""), false, gatewayOnly(), rec))

	assert.Equal(t, 1, cortex.runs)
	assert.Zero(t, fallback.runs, "a resolved gateway credential keeps the turn on the Cortex executor")
	assert.Equal(t, "https://docs.example/cortex", gjson.GetBytes(rec.Body.Bytes(), "content.1.content.0.url").String())
}

func TestServeNativeWebSearchGatewayFailureDoesNotUseFallback(t *testing.T) {
	cortex := &fakeSearch{err: context.DeadlineExceeded}
	fallback := &fakeSearch{resp: fallbackSearchResult()}
	s := &Service{webSearch: webSearchExecutors{gateway: cortex, fallback: fallback}}
	rec := httptest.NewRecorder()

	assert.False(t, serveTestMessagesWebSearch(t, gatewayCtx(), s, claudeCodeSearchHelperBody(false, ""), false, gatewayOnly(), rec))
	assert.Equal(t, 1, cortex.runs)
	assert.Zero(t, fallback.runs)
	assert.Zero(t, rec.Body.Len())
}

func TestServeNativeWebSearchNoGatewayCredentialUsesFallback(t *testing.T) {
	cortex := &fakeSearch{resp: websearch.Response{Results: []websearch.Result{{Title: "Cortex", URL: "https://docs.example/cortex"}}}}
	fallback := &fakeSearch{resp: fallbackSearchResult()}
	s := &Service{webSearch: webSearchExecutors{gateway: cortex, fallback: fallback}}
	rec := httptest.NewRecorder()

	require.True(t, serveTestMessagesWebSearch(t, t.Context(), s, claudeCodeSearchHelperBody(false, ""), false, nonAnthropicProviders(), rec))

	assert.Zero(t, cortex.runs, "the Cortex executor needs a gateway credential")
	assert.Equal(t, 1, fallback.runs)
	assert.Equal(t, "https://docs.example/releases", gjson.GetBytes(rec.Body.Bytes(), "content.1.content.0.url").String())
}

func TestServeNativeWebSearchFallbackFailureFallsThrough(t *testing.T) {
	fallback := &fakeSearch{err: context.DeadlineExceeded}
	s := &Service{webSearch: webSearchExecutors{fallback: fallback}}
	rec := httptest.NewRecorder()

	assert.False(t, serveTestMessagesWebSearch(t, t.Context(), s, claudeCodeSearchHelperBody(false, ""), false, nonAnthropicProviders(), rec))
	assert.Equal(t, 1, fallback.runs)
	assert.Zero(t, rec.Body.Len(), "nothing may be written before falling through")
}

func TestProxyMessagesClaudeCodeWebSearchFallback(t *testing.T) {
	newService := func() (*Service, map[string]*bypassFakeProvider) {
		upstreams := map[string]*bypassFakeProvider{
			providers.ProviderOpenAI: {}, providers.ProviderGoogle: {}, providers.ProviderOpenRouter: {},
		}
		clients := make(map[string]providers.Client, len(upstreams))
		for name, p := range upstreams {
			clients[name] = p
		}
		svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5"}}, clients, nil, nil, false, providers.ProviderOpenAI, "gpt-5", nil)
		return svc, upstreams
	}
	body := claudeCodeSearchHelperBody(false, "")

	t.Run("without fallback executor", func(t *testing.T) {
		svc, upstreams := newService()
		rec := httptest.NewRecorder()
		err := svc.ProxyMessages(context.Background(), body, rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, routePathMessages, strings.NewReader(string(body))))

		require.ErrorIs(t, err, ErrTranslationCompatibleProviderUnavailable)
		assert.Contains(t, err.Error(), `no compatible provider is available for translation requirements: target family "anthropic"`)
		assert.NotContains(t, rec.Body.String(), "web_search_tool_result")
		for name, p := range upstreams {
			assert.Zero(t, p.dispatches, "%s must not receive an Anthropic server-tool turn", name)
		}
	})

	t.Run("with fallback executor", func(t *testing.T) {
		svc, upstreams := newService()
		fallback := &fakeSearch{resp: fallbackSearchResult()}
		svc.WithWebSearchExecutors(nil, fallback)
		rec := httptest.NewRecorder()
		err := svc.ProxyMessages(context.Background(), body, rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, routePathMessages, strings.NewReader(string(body))))

		require.NoError(t, err)
		assert.Equal(t, 1, fallback.runs)
		assert.Equal(t, "router release notes", fallback.got.Text)
		body := gjson.ParseBytes(rec.Body.Bytes())
		assert.Equal(t, "message", body.Get("type").String())
		assert.Equal(t, "server_tool_use", body.Get("content.0.type").String())
		assert.Equal(t, "web_search_tool_result", body.Get("content.1.type").String())
		assert.Equal(t, "https://docs.example/releases", body.Get("content.1.content.0.url").String())
		for name, p := range upstreams {
			assert.Zero(t, p.dispatches, "%s must not be dispatched when the fallback serves the turn", name)
		}
	})

	for name, extra := range map[string]string{
		"mixed tools":                    "",
		"mixed tools with domain filter": `,"allowed_domains":["docs.example"]`,
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newService()
			fallback := &fakeSearch{resp: fallbackSearchResult()}
			svc.WithWebSearchExecutors(nil, fallback)
			body := strings.Replace(string(claudeCodeSearchHelperBody(false, extra)), `"tools":[`, `"tools":[{"name":"Read","input_schema":{"type":"object"}},`, 1)
			rec := httptest.NewRecorder()

			err := svc.ProxyMessages(t.Context(), []byte(body), rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, routePathMessages, strings.NewReader(body)))

			require.ErrorIs(t, err, ErrTranslationCompatibleProviderUnavailable)
			assert.Zero(t, fallback.runs)
			assert.NotContains(t, rec.Body.String(), "web_search_tool_result")
		})
	}
}
