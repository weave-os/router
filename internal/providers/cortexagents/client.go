// Package cortexagents executes web searches on Snowflake Cortex Agents
// (agent:run). Cortex's inference endpoints reject Anthropic's native
// web_search server tool; the Agents endpoint on the same host and credential
// does not, keeping gateway-tenant traffic inside their contracted provider.
package cortexagents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/websearch"
)

// runPath is the stateless agent endpoint: no agent object required, the spec
// travels inline with the request.
const runPath = "/agent:run"

// searchToolName names the inline web_search tool; tool_resources is keyed by it.
const searchToolName = "web_search_1"

// responseInstruction keeps the agent's prose short — the calling model only
// needs the findings, and Claude Code renders the result blocks itself.
const responseInstruction = "Answer the search query directly and concisely from the web results. " +
	"Cite the source URLs you used. Do not ask follow-up questions."

// defaultHostSuffix confines agent:run to Snowflake. The executor is selected
// on capability (a gateway that cannot run server tools), which also matches
// non-Cortex gateways where agent:run is not a route at all.
const defaultHostSuffix = "snowflakecomputing.com"

// defaultTimeout bounds one agent run. Agent orchestration is slower than a
// raw search API, and the caller is a blocked client turn; the budget is set
// well clear of the observed tail so expiry doesn't cost the turn a 400.
const defaultTimeout = 90 * time.Second

// Option configures a Client at construction.
type Option func(*Client)

// WithRole sets the Snowflake role the agent runs as. Empty leaves the header
// off, and Snowflake falls back to the service user's default role.
func WithRole(role string) Option {
	return func(c *Client) { c.role = strings.TrimSpace(role) }
}

// WithHostSuffix overrides the host suffix a gateway base URL must match for
// the search to be attempted.
func WithHostSuffix(suffix string) Option {
	return func(c *Client) { c.hostSuffix = strings.ToLower(strings.TrimSpace(suffix)) }
}

// WithTimeout overrides the per-search timeout. Zero or negative is ignored.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// Client runs Cortex Agent searches against the tenant's own account, using
// the per-request gateway credential carried on the context.
type Client struct {
	baseURL    string
	role       string
	hostSuffix string
	timeout    time.Duration
	http       *http.Client
}

// NewClient constructs a Cortex Agents search client. baseURL is the fallback
// Cortex base (e.g. https://acct.snowflakecomputing.com/api/v2/cortex); a BYOK
// credential's own base URL takes precedence per request.
func NewClient(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		hostSuffix: defaultHostSuffix,
		timeout:    defaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	// agent:run buffers the whole response before the first byte; ResponseHeaderTimeout
	// must equal the run budget rather than the streaming-upstream constant.
	c.http = httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(10*time.Second, 10*time.Second, c.timeout))
	return c
}

// Search implements websearch.Executor.
func (c *Client) Search(ctx context.Context, q websearch.Query) (websearch.Response, error) {
	query := strings.TrimSpace(q.Text)
	if query == "" {
		return websearch.Response{}, fmt.Errorf("cortexagents: empty query")
	}
	creds := proxy.CredentialsFromContext(ctx)
	if creds == nil || len(creds.APIKey) == 0 {
		return websearch.Response{}, fmt.Errorf("cortexagents: no credential on request")
	}
	baseURL := proxy.EffectiveBaseURL(ctx, c.baseURL)
	if baseURL == "" {
		return websearch.Response{}, fmt.Errorf("cortexagents: no base URL configured")
	}
	if err := c.checkHost(baseURL); err != nil {
		return websearch.Response{}, err
	}

	body, err := json.Marshal(runRequest(query, q.MaxResults))
	if err != nil {
		return websearch.Response{}, fmt.Errorf("cortexagents: marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agentRunURL(baseURL), bytes.NewReader(body))
	if err != nil {
		return websearch.Response{}, fmt.Errorf("cortexagents: build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("authorization", "Bearer "+string(creds.APIKey))
	if creds.AuthType == auth.AuthTypeWIF {
		req.Header.Set(auth.WIFTokenTypeHeader, auth.WIFTokenTypeValue)
	}
	if c.role != "" {
		req.Header.Set("X-Snowflake-Role", c.role)
	}
	// Correlation headers aren't on req; pull them from the ingress snapshot on ctx.
	proxy.ApplyForwardedClientHeaders(ctx, req, nil)

	resp, err := c.http.Do(req)
	if err != nil {
		return websearch.Response{}, fmt.Errorf("cortexagents: agent:run call: %w", err)
	}
	defer resp.Body.Close()

	raw, _, readErr := httputil.ReadCapped(resp.Body, maxResponseBytes)
	if resp.StatusCode >= 400 {
		return websearch.Response{}, fmt.Errorf("cortexagents: agent:run status %d: %s", resp.StatusCode, httputil.PreviewBytes(raw))
	}
	if readErr != nil {
		return websearch.Response{}, fmt.Errorf("cortexagents: read response: %w", readErr)
	}
	return parseRunResponse(raw, q.MaxResults), nil
}

// checkHost rejects a gateway that is not the Snowflake endpoint agent:run
// belongs to.
func (c *Client) checkHost(baseURL string) error {
	if c.hostSuffix == "" {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("cortexagents: parse base URL: %w", err)
	}
	// DNS is case-insensitive; a BYOK base URL is whatever the tenant typed.
	host := strings.ToLower(u.Hostname())
	if host != c.hostSuffix && !strings.HasSuffix(host, "."+c.hostSuffix) {
		return fmt.Errorf("cortexagents: gateway host %q is not %s", host, c.hostSuffix)
	}
	return nil
}

// agentRunURL places agent:run under the Cortex root. The inference surfaces
// hang off /v1 and a tenant's configured base URL may include that segment,
// but agent:run does not live under it.
func agentRunURL(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1") + runPath
}

// maxResponseBytes caps the buffered agent response; an agent answer plus
// twenty results is far below this.
const maxResponseBytes = 1 << 20

// runRequest builds the inline (agent-object-free) agent spec: one web_search
// tool, one user message, non-streaming.
func runRequest(query string, maxResults int) map[string]any {
	if maxResults <= 0 {
		maxResults = websearch.DefaultMaxResults
	}
	return map[string]any{
		"stream": false,
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": query}},
			},
		},
		"instructions": map[string]any{"response": responseInstruction},
		"tools": []any{
			map[string]any{
				"tool_spec": map[string]any{"type": "web_search", "name": searchToolName},
			},
		},
		"tool_resources": map[string]any{
			searchToolName: map[string]any{"max_results": maxResults},
		},
	}
}

// parseRunResponse extracts the agent's answer text and search hits.
// Snowflake documents the envelope but not the web_search tool_result payload,
// so hits are collected structurally (any URL-bearing object) rather than from
// a fixed path.
func parseRunResponse(raw []byte, maxResults int) websearch.Response {
	if maxResults <= 0 {
		maxResults = websearch.DefaultMaxResults
	}
	root := gjson.ParseBytes(raw)
	var out websearch.Response
	var summary strings.Builder
	root.Get("content").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "text" {
			if text := strings.TrimSpace(item.Get("text").String()); text != "" {
				if summary.Len() > 0 {
					summary.WriteString("\n\n")
				}
				summary.WriteString(text)
			}
		}
		return true
	})
	out.Summary = summary.String()
	out.Results = collectResults(root, maxResults)
	return out
}

// collectResults walks the response for objects that look like a web hit,
// preserving document order and dropping duplicate URLs.
func collectResults(node gjson.Result, limit int) []websearch.Result {
	seen := make(map[string]struct{})
	var results []websearch.Result
	var walk func(gjson.Result)
	walk = func(n gjson.Result) {
		if len(results) >= limit {
			return
		}
		if n.IsObject() {
			if r, ok := resultFrom(n); ok {
				if _, dup := seen[r.URL]; !dup {
					seen[r.URL] = struct{}{}
					results = append(results, r)
				}
			}
		}
		n.ForEach(func(_, child gjson.Result) bool {
			if child.IsObject() || child.IsArray() {
				walk(child)
			}
			return len(results) < limit
		})
	}
	walk(node)
	return results
}

// resultFrom reads a web hit out of an object, tolerating the field naming
// Snowflake's search backends use interchangeably.
func resultFrom(obj gjson.Result) (websearch.Result, bool) {
	url := firstString(obj, "url", "source_url", "link", "document_url")
	if !strings.HasPrefix(url, "http") {
		return websearch.Result{}, false
	}
	return websearch.Result{
		Title:   firstString(obj, "title", "doc_title", "name", "page_title"),
		URL:     url,
		Snippet: truncate(firstString(obj, "snippet", "description", "summary", "content", "text"), maxSnippetChars),
		PageAge: firstString(obj, "page_age", "published_date", "last_updated"),
	}, true
}

// maxSnippetChars keeps a verbose page extract from dominating the answer text.
const maxSnippetChars = 400

func firstString(obj gjson.Result, keys ...string) string {
	for _, key := range keys {
		if v := obj.Get(key); v.Type == gjson.String {
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "…"
}

var _ websearch.Executor = (*Client)(nil)
