package cortexagents_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers/cortexagents"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/websearch"
)

// wifContext mirrors what the proxy injects for a WIF-authenticated gateway key.
func wifContext(baseURL string) context.Context {
	return context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey:   []byte("WIF.GCP.attestation-token"),
		BaseURL:  baseURL,
		AuthType: auth.AuthTypeWIF,
		Source:   "gateway",
	})
}

const agentResponse = `{
  "role": "assistant",
  "content": [
    {"type": "tool_result", "tool_result": {"content": [{"type": "json", "json": {"searchResults": [
        {"title": "Cortex Agents", "url": "https://docs.snowflake.com/agents", "snippet": "Agents run tools."},
        {"title": "Web search tool", "source_url": "https://docs.snowflake.com/web-search", "description": "Brave index."},
        {"title": "Duplicate", "url": "https://docs.snowflake.com/agents"}
    ]}}]}},
    {"type": "text", "text": "Cortex Agents provide a native web_search tool."}
  ],
  "status": "completed"
}`

func TestSearchSendsWIFAuthenticatedAgentRun(t *testing.T) {
	var gotPath, gotAuth, gotTokenType, gotRole string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		gotTokenType = r.Header.Get(auth.WIFTokenTypeHeader)
		gotRole = r.Header.Get("X-Snowflake-Role")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, agentResponse)
	}))
	defer srv.Close()

	client := cortexagents.NewClient("", cortexagents.WithRole("ROUTER_AGENT"), cortexagents.WithHostSuffix("127.0.0.1"))
	resp, err := client.Search(wifContext(srv.URL+"/api/v2/cortex"), websearch.Query{Text: "cortex agents web search", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if gotPath != "/api/v2/cortex/agent:run" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer WIF.GCP.attestation-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotTokenType != auth.WIFTokenTypeValue {
		t.Fatalf("%s = %q; Snowflake rejects a WIF bearer without it", auth.WIFTokenTypeHeader, gotTokenType)
	}
	if gotRole != "ROUTER_AGENT" {
		t.Fatalf("X-Snowflake-Role = %q", gotRole)
	}

	req := gjson.ParseBytes(gotBody)
	if req.Get("stream").Bool() {
		t.Fatal("agent run must be non-streaming")
	}
	if got := req.Get("tools.0.tool_spec.type").String(); got != "web_search" {
		t.Fatalf("tool spec type = %q", got)
	}
	toolName := req.Get("tools.0.tool_spec.name").String()
	if got := req.Get("tool_resources." + toolName + ".max_results").Int(); got != 5 {
		t.Fatalf("max_results = %d (tool_resources must be keyed by the tool name %q)", got, toolName)
	}
	if got := req.Get("messages.0.content.0.text").String(); got != "cortex agents web search" {
		t.Fatalf("query text = %q", got)
	}

	if resp.Summary != "Cortex Agents provide a native web_search tool." {
		t.Fatalf("summary = %q", resp.Summary)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2 deduped hits: %+v", len(resp.Results), resp.Results)
	}
	if resp.Results[0].URL != "https://docs.snowflake.com/agents" || resp.Results[0].Snippet != "Agents run tools." {
		t.Fatalf("first result = %+v", resp.Results[0])
	}
	if resp.Results[1].URL != "https://docs.snowflake.com/web-search" || resp.Results[1].Snippet != "Brave index." {
		t.Fatalf("second result = %+v (source_url/description aliases must be read)", resp.Results[1])
	}
}

func TestSearchDropsInferenceVersionSegmentFromBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{"content":[]}`)
	}))
	defer srv.Close()

	// A gateway key configured for /v1/messages carries the /v1 segment;
	// agent:run is mounted on the Cortex root instead.
	if _, err := cortexagents.NewClient("", cortexagents.WithHostSuffix("127.0.0.1")).Search(wifContext(srv.URL+"/api/v2/cortex/v1"), websearch.Query{Text: "q"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotPath != "/api/v2/cortex/agent:run" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestSearchCapsResultsAtRequestedMax(t *testing.T) {
	hits := make([]map[string]string, 0, 8)
	for i := 0; i < 8; i++ {
		hits = append(hits, map[string]string{"title": "hit", "url": "https://example.com/" + string(rune('a'+i))})
	}
	payload, err := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "tool_result", "results": hits}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(payload) }))
	defer srv.Close()

	resp, err := cortexagents.NewClient("", cortexagents.WithHostSuffix("127.0.0.1")).Search(wifContext(srv.URL), websearch.Query{Text: "q", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %d, want 3", len(resp.Results))
	}
}

func TestSearchSurfacesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"Agent web search is not enabled for this account"}`)
	}))
	defer srv.Close()

	_, err := cortexagents.NewClient("", cortexagents.WithHostSuffix("127.0.0.1")).Search(wifContext(srv.URL), websearch.Query{Text: "q"})
	if err == nil {
		t.Fatal("expected an error for a 403")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("error must carry the upstream diagnosis, got %v", err)
	}
}

func TestSearchRequiresCredentialAndQuery(t *testing.T) {
	client := cortexagents.NewClient("https://acct.snowflakecomputing.com/api/v2/cortex")
	if _, err := client.Search(context.Background(), websearch.Query{Text: "q"}); err == nil {
		t.Fatal("expected an error without a credential on the context")
	}
	if _, err := client.Search(wifContext("https://acct.example"), websearch.Query{Text: "  "}); err == nil {
		t.Fatal("expected an error for an empty query")
	}
}

func TestSearchRefusesNonSnowflakeGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a non-Snowflake gateway must never be probed for agent:run")
		io.WriteString(w, `{"content":[]}`)
	}))
	defer srv.Close()

	_, err := cortexagents.NewClient("").Search(wifContext(srv.URL), websearch.Query{Text: "q"})
	if err == nil || !strings.Contains(err.Error(), "not snowflakecomputing.com") {
		t.Fatalf("err = %v, want a host rejection", err)
	}
}

func TestSearchAcceptsMixedCaseGatewayHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"content":[]}`)
	}))
	defer srv.Close()

	// DNS is case-insensitive, and a BYOK base URL is whatever the tenant typed.
	url := strings.Replace(srv.URL, "127.0.0.1", "LocalHost", 1)
	if _, err := cortexagents.NewClient("", cortexagents.WithHostSuffix("LOCALHOST")).Search(wifContext(url), websearch.Query{Text: "q"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSearchToleratesSlowFirstByte(t *testing.T) {
	// An agent run buffers everything before the first byte; a time-to-first-byte
	// guard shorter than the run budget expires it and costs the turn a 400.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		io.WriteString(w, agentResponse)
	}))
	defer srv.Close()

	client := cortexagents.NewClient("",
		cortexagents.WithHostSuffix("127.0.0.1"),
		cortexagents.WithTimeout(5*time.Second),
	)
	resp, err := client.Search(wifContext(srv.URL), websearch.Query{Text: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected the buffered response to be parsed")
	}
}

func TestSearchFailsFastPastItsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		io.WriteString(w, agentResponse)
	}))
	defer srv.Close()

	client := cortexagents.NewClient("",
		cortexagents.WithHostSuffix("127.0.0.1"),
		cortexagents.WithTimeout(100*time.Millisecond),
	)
	if _, err := client.Search(wifContext(srv.URL), websearch.Query{Text: "q"}); err == nil {
		t.Fatal("expected the run budget to bound a hung agent")
	}
}

func TestSearchOmitsRoleHeaderAndTokenTypeWhenNotApplicable(t *testing.T) {
	var hasRole, hasTokenType bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasRole = r.Header["X-Snowflake-Role"]
		_, hasTokenType = r.Header[http.CanonicalHeaderKey(auth.WIFTokenTypeHeader)]
		io.WriteString(w, `{"content":[]}`)
	}))
	defer srv.Close()

	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey:   []byte("static-pat"),
		BaseURL:  srv.URL,
		AuthType: auth.AuthTypeBearer,
	})
	if _, err := cortexagents.NewClient("", cortexagents.WithHostSuffix("127.0.0.1")).Search(ctx, websearch.Query{Text: "q"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if hasRole {
		t.Fatal("no role configured, header must be absent")
	}
	if hasTokenType {
		t.Fatalf("%s must only be sent for WIF credentials", auth.WIFTokenTypeHeader)
	}
}

func TestSearchForwardsClientCorrelationHeaders(t *testing.T) {
	var gotApp, gotBaggage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotApp = r.Header.Get("X-SNOWFLAKE-APPLICATION")
		gotBaggage = r.Header.Get("X-SNOWFLAKE-BAGGAGE")
		io.WriteString(w, `{"content":[]}`)
	}))
	defer srv.Close()

	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey:                 []byte("static-pat"),
		BaseURL:                srv.URL,
		AuthType:               auth.AuthTypeBearer,
		ForwardedClientHeaders: []string{"X-SNOWFLAKE-APPLICATION"},
		BaggageHeader:          "X-SNOWFLAKE-BAGGAGE",
	})
	ctx = context.WithValue(ctx, proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{Email: "engineer@example.com"})
	inbound := http.Header{}
	inbound.Set("X-SNOWFLAKE-APPLICATION", "cortex-cli/1.2.3")
	ctx = proxy.WithForwardedHeaderSnapshot(ctx, []*auth.ExternalAPIKey{{
		ForwardedClientHeaders: []string{"X-SNOWFLAKE-APPLICATION"},
	}}, inbound)

	if _, err := cortexagents.NewClient("", cortexagents.WithHostSuffix("127.0.0.1")).Search(ctx, websearch.Query{Text: "q"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotApp != "cortex-cli/1.2.3" {
		t.Fatalf("agent:run runs on the tenant's endpoint and must carry the caller's application: %q", gotApp)
	}
	if gotBaggage != `{"on-behalf-of":"engineer@example.com"}` {
		t.Fatalf("unexpected baggage: %q", gotBaggage)
	}
}
