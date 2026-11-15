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
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/websearch"
)

const runPath = "/agent:run"

const searchToolName = "web_search_1"

const responseInstruction = "Answer the search query directly and concisely from the web results. " +
	"Cite the source URLs you used. Do not ask follow-up questions."

const defaultHostSuffix = "snowflakecomputing.com"

const defaultTimeout = 90 * time.Second

type Option func(*Client)

func WithRole(role string) Option {
	return func(c *Client) { c.role = strings.TrimSpace(role) }
}

func WithHostSuffix(suffix string) Option {
	return func(c *Client) { c.hostSuffix = strings.ToLower(strings.TrimSpace(suffix)) }
}

func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

type Client struct {
	baseURL    string
	role       string
	hostSuffix string
	timeout    time.Duration
	http       *http.Client
}

func NewClient(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		hostSuffix: defaultHostSuffix,
		timeout:    defaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	c.http = httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(10*time.Second, 10*time.Second, c.timeout))
	return c
}

func (c *Client) Search(ctx context.Context, q websearch.Query) (websearch.Response, error) {
	query := strings.TrimSpace(q.Text)
	if query == "" {
		return websearch.Response{}, fmt.Errorf("cortexagents: empty query")
	}
	creds := requestcontext.CredentialsFromContext(ctx)
	if creds == nil || len(creds.APIKey) == 0 {
		return websearch.Response{}, fmt.Errorf("cortexagents: no credential on request")
	}
	baseURL := requestcontext.EffectiveBaseURL(ctx, c.baseURL)
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
	requestcontext.ApplyForwardedClientHeaders(ctx, req, nil)

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

func (c *Client) checkHost(baseURL string) error {
	if c.hostSuffix == "" {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("cortexagents: parse base URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host != c.hostSuffix && !strings.HasSuffix(host, "."+c.hostSuffix) {
		return fmt.Errorf("cortexagents: gateway host %q is not %s", host, c.hostSuffix)
	}
	return nil
}

func agentRunURL(baseURL string) string {
	return providers.CortexRESTRoot(baseURL) + runPath
}

const maxResponseBytes = 1 << 20

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
