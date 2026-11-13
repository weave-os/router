package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/websearch"
)

type fakeSearch struct {
	got  websearch.Query
	resp websearch.Response
	err  error
	runs int
}

func (f *fakeSearch) Search(_ context.Context, q websearch.Query) (websearch.Response, error) {
	f.runs++
	f.got = q
	return f.resp, f.err
}

const searchTurnBody = `{
	"model":"claude-sonnet-5",
	"stream":false,
	"tools":[{"type":"web_search_20250305","name":"web_search"}],
	"messages":[{"role":"user","content":"Perform a web search for the query: cortex agents"}]
}`

func gatewayCtx() context.Context {
	return context.WithValue(context.Background(), CredentialsContextKey{}, &Credentials{
		APIKey:   []byte("WIF.GCP.token"),
		BaseURL:  "https://acct.snowflakecomputing.com/api/v2/cortex",
		AuthType: auth.AuthTypeWIF,
	})
}

func gatewayOnly() map[string]struct{} {
	return map[string]struct{}{providers.ProviderAnthropicGateway: {}, providers.ProviderOpenAIGateway: {}}
}

func TestServeNativeWebSearchAnswersGatewayOnlyTenant(t *testing.T) {
	ex := &fakeSearch{resp: websearch.Response{
		Summary: "Agents run web_search.",
		Results: []websearch.Result{{Title: "Docs", URL: "https://docs.snowflake.com/agents"}},
	}}
	s := &Service{webSearch: ex}
	rec := httptest.NewRecorder()

	if !s.serveNativeWebSearch(gatewayCtx(), []byte(searchTurnBody), "claude-sonnet-5", false, 42, gatewayOnly(), http.Header{}, rec) {
		t.Fatal("search turn was not served")
	}
	if ex.got.Text != "cortex agents" {
		t.Fatalf("executed query = %q", ex.got.Text)
	}
	body := gjson.ParseBytes(rec.Body.Bytes())
	if got := body.Get("content.0.type").String(); got != "server_tool_use" {
		t.Fatalf("first block = %q", got)
	}
	if got := body.Get("content.1.content.0.url").String(); got != "https://docs.snowflake.com/agents" {
		t.Fatalf("result url = %q", got)
	}
	if got := body.Get("model").String(); got != "claude-sonnet-5" {
		t.Fatalf("model = %q; the client must see the model it asked for", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestServeNativeWebSearchStreams(t *testing.T) {
	ex := &fakeSearch{resp: websearch.Response{
		Summary: "Answer.",
		Results: []websearch.Result{{Title: "Docs", URL: "https://docs.snowflake.com/agents"}},
	}}
	s := &Service{webSearch: ex}
	rec := httptest.NewRecorder()

	if !s.serveNativeWebSearch(gatewayCtx(), []byte(searchTurnBody), "claude-sonnet-5", true, 10, gatewayOnly(), http.Header{}, rec) {
		t.Fatal("search turn was not served")
	}
	out := rec.Body.String()
	for _, want := range []string{
		"event: message_start", "event: content_block_start", "event: message_stop",
		`"type":"input_json_delta"`, `"type":"web_search_tool_result"`, `"type":"text_delta"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `{\"query\":\"cortex agents\"}`) {
		t.Fatalf("server_tool_use input must stream as a delta:\n%s", out)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestServeNativeWebSearchDefersToNativelyCapableProvider(t *testing.T) {
	ex := &fakeSearch{}
	s := &Service{webSearch: ex}
	enabled := map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderAnthropicGateway: {}}

	if s.serveNativeWebSearch(gatewayCtx(), []byte(searchTurnBody), "claude-sonnet-5", false, 0, enabled, http.Header{}, httptest.NewRecorder()) {
		t.Fatal("vendor Anthropic runs the tool itself; the turn must stay on normal routing")
	}
	if ex.runs != 0 {
		t.Fatal("executor must not run when a capable provider is available")
	}
}

func TestServeNativeWebSearchFallsThroughOnExecutorFailure(t *testing.T) {
	ex := &fakeSearch{err: context.DeadlineExceeded}
	s := &Service{webSearch: ex}
	rec := httptest.NewRecorder()

	if s.serveNativeWebSearch(gatewayCtx(), []byte(searchTurnBody), "claude-sonnet-5", false, 0, gatewayOnly(), http.Header{}, rec) {
		t.Fatal("a failed search must not claim the turn")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("nothing may be written before falling through: %q", rec.Body.String())
	}
}

func TestServeNativeWebSearchIgnoresTurnsWithoutTheTool(t *testing.T) {
	ex := &fakeSearch{}
	s := &Service{webSearch: ex}
	body := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"Perform a web search for the query: x"}]}`)

	if s.serveNativeWebSearch(gatewayCtx(), body, "claude-sonnet-5", false, 0, gatewayOnly(), http.Header{}, httptest.NewRecorder()) {
		t.Fatal("no native tool declared; nothing to serve")
	}
	if ex.runs != 0 {
		t.Fatal("executor ran without a declared server tool")
	}
}

func TestServeNativeWebSearchDisabled(t *testing.T) {
	s := &Service{}
	if s.serveNativeWebSearch(gatewayCtx(), []byte(searchTurnBody), "claude-sonnet-5", false, 0, gatewayOnly(), http.Header{}, httptest.NewRecorder()) {
		t.Fatal("no executor wired; the turn must stay on normal routing")
	}
}
