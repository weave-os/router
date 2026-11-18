package keenable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/websearch"
)

const DefaultBaseURL = "https://api.keenable.ai"

const (
	searchPath       = "/v1/search"
	publicSearchPath = "/v1/search/public"
)

const defaultAppTitle = "weave-router"

const defaultTimeout = 30 * time.Second

const maxResponseBytes = 1 << 20

const maxSnippetChars = 400

type Option func(*Client)

func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = strings.TrimSpace(key) }
}

func WithAppTitle(title string) Option {
	return func(c *Client) {
		if title = strings.TrimSpace(title); title != "" {
			c.appTitle = title
		}
	}
}

func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

type Client struct {
	baseURL  string
	apiKey   string
	appTitle string
	timeout  time.Duration
	http     *http.Client
}

func NewClient(baseURL string, opts ...Option) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	c := &Client{
		baseURL:  baseURL,
		appTitle: defaultAppTitle,
		timeout:  defaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	c.http = httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(10*time.Second, 10*time.Second, c.timeout))
	return c
}

func (c *Client) Keyless() bool { return c.apiKey == "" }

func (c *Client) Search(ctx context.Context, q websearch.Query) (websearch.Response, error) {
	query := strings.TrimSpace(q.Text)
	if query == "" {
		return websearch.Response{}, fmt.Errorf("keenable: empty query")
	}
	limit := resultLimit(q.MaxResults)

	body, err := json.Marshal(searchRequest{Query: query, MaxResults: limit})
	if err != nil {
		return websearch.Response{}, fmt.Errorf("keenable: marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.searchURL(), bytes.NewReader(body))
	if err != nil {
		return websearch.Response{}, fmt.Errorf("keenable: build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("X-Keenable-Title", c.appTitle)
	if !c.Keyless() {
		req.Header.Set("authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return websearch.Response{}, fmt.Errorf("keenable: search call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _, readErr := httputil.ReadCapped(resp.Body, maxResponseBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return websearch.Response{}, &StatusError{StatusCode: resp.StatusCode, Preview: httputil.PreviewBytes(raw)}
	}
	if readErr != nil {
		return websearch.Response{}, fmt.Errorf("keenable: read response: %w", readErr)
	}
	if !gjson.ValidBytes(raw) {
		return websearch.Response{}, fmt.Errorf("keenable: invalid JSON response")
	}
	return parseSearchResponse(raw, limit), nil
}

type StatusError struct {
	StatusCode int
	Preview    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("keenable: search status %d: %s", e.StatusCode, e.Preview)
}

type searchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (c *Client) searchURL() string {
	if c.Keyless() {
		return c.baseURL + publicSearchPath
	}
	return c.baseURL + searchPath
}

func resultLimit(requested int) int {
	if requested <= 0 || requested > websearch.DefaultMaxResults {
		return websearch.DefaultMaxResults
	}
	return requested
}

func parseSearchResponse(raw []byte, limit int) websearch.Response {
	var out websearch.Response
	seen := make(map[string]struct{})
	gjson.GetBytes(raw, "results").ForEach(func(_, item gjson.Result) bool {
		url := strings.TrimSpace(item.Get("url").String())
		if !strings.HasPrefix(url, "http") {
			return true
		}
		if _, dup := seen[url]; dup {
			return true
		}
		seen[url] = struct{}{}
		out.Results = append(out.Results, websearch.Result{
			Title:   strings.TrimSpace(item.Get("title").String()),
			URL:     url,
			Snippet: truncate(firstString(item, "snippet", "description"), maxSnippetChars),
			PageAge: strings.TrimSpace(item.Get("published_at").String()),
		})
		return len(out.Results) < limit
	})
	return out
}

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
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max])) + "…"
}

var _ websearch.Executor = (*Client)(nil)
