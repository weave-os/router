package websearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
	"golang.org/x/net/idna"
)

// ResponsesRequest translates an isolated Claude Code search to native Responses tools.
func ResponsesRequest(body []byte, model string) ([]byte, error) {
	query, ok := DetectSearchTurn(body)
	if !ok || !IsClaudeCodeWebSearchHelper(body) {
		return nil, errors.New("not an isolated Claude Code web search")
	}
	tool := gjson.GetBytes(body, "tools.0")
	search := map[string]any{"type": "web_search"}
	filters := map[string]any{}
	for _, name := range []string{"allowed_domains", "blocked_domains"} {
		if value := tool.Get(name); value.Exists() {
			if !value.IsArray() {
				return nil, errors.New("web search domain filters must be arrays")
			}
			if len(value.Array()) > 100 {
				return nil, errors.New("web search domain filters support at most 100 domains")
			}
			normalized := make([]string, 0, len(value.Array()))
			for _, domain := range value.Array() {
				raw := domain.String()
				if domain.Type != gjson.String || raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "/*:?#@[]\\ \t\r\n") {
					return nil, errors.New("Codex web search requires hostname-only domain filters")
				}
				ascii, err := idna.Lookup.ToASCII(strings.TrimSuffix(raw, "."))
				if err != nil || ascii == "" || strings.HasPrefix(ascii, ".") || strings.Contains(ascii, "..") || strings.HasSuffix(ascii, ".") {
					return nil, errors.New("invalid web search domain filter")
				}
				normalized = append(normalized, strings.ToLower(ascii))
			}
			if len(normalized) > 0 {
				filters[name] = normalized
			}
		}
	}
	if len(filters) > 0 {
		search["filters"] = filters
	}
	if location := tool.Get("user_location"); location.Exists() {
		search["user_location"] = location.Value()
	}
	request := map[string]any{
		"model": model, "stream": true, "store": false,
		"instructions": fmt.Sprintf("Use web_search to search for the user's query. Use at most %d tool operations. Return a concise answer with source citations.", CallLimit(body)),
		"input":        []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": claudeCodeSearchPrompt + query.Text}}}},
		"tools":        []any{search}, "tool_choice": "auto",
		"include":   []string{"web_search_call.action.sources"},
		"reasoning": map[string]any{"effort": "low"},
	}
	if limit := tool.Get("max_uses"); limit.Exists() {
		if limit.Type != gjson.Number || limit.Int() <= 0 || float64(limit.Int()) != limit.Num || limit.Int() > MaxNativeSearchOperations {
			return nil, errors.New("web search max_uses must be an integer between 1 and 10")
		}

	}
	return json.Marshal(request)
}

// ParseResponses collects completed search items, citations and measured usage.
// Codex may leave terminal output empty after delivering output_item.done events.
func ParseResponses(body, request []byte) (Response, error) {
	items := map[int]gjson.Result{}
	var terminal gjson.Result
	for len(body) > 0 {
		event, n := sse.SplitNext(body)
		if n == 0 {
			break
		}
		body = body[n:]
		_, data := sse.ParseEvent(event)
		if len(data) == 0 {
			continue
		}
		value := gjson.ParseBytes(data)
		switch value.Get("type").String() {
		case "response.output_item.done":
			items[int(value.Get("output_index").Int())] = value.Get("item")
		case "response.completed":
			terminal = value.Get("response")
		case "response.failed", "response.incomplete", "error":
			details := value.Get("response.error")
			if !details.Exists() {
				details = value.Get("error")
			}
			if !details.Exists() {
				details = value
			}
			code, message := details.Get("code").String(), details.Get("message").String()
			if code == "" {
				code = value.Get("response.incomplete_details.reason").String()
			}
			return Response{}, fmt.Errorf("web search did not complete (%s): %s", code, message)
		}
	}
	if !terminal.Exists() || terminal.Get("status").String() != "completed" {
		return Response{}, errors.New("web search response has no successful terminal event")
	}
	if output := terminal.Get("output").Array(); len(output) > 0 {
		items = make(map[int]gjson.Result, len(output))
		for i, item := range output {
			items[i] = item
		}
	}
	indexes := make([]int, 0, len(items))
	for i := range items {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	response := Response{Model: terminal.Get("model").String()}
	tool := gjson.GetBytes(request, "tools.0")
	allowed, blocked := tool.Get("allowed_domains").Array(), tool.Get("blocked_domains").Array()
	violated := false
	seen := map[string]int{}
	allowedURL := func(rawURL string) bool {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			if len(allowed) > 0 || len(blocked) > 0 {
				violated = true
			}
			return false
		}
		host, normalizeErr := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(parsed.Hostname()), "."))
		if normalizeErr != nil {
			if len(allowed) > 0 || len(blocked) > 0 {
				violated = true
			}
			return false
		}
		matches := func(domains []gjson.Result) bool {
			for _, domain := range domains {
				d, normalizeErr := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(domain.String()), "."))
				if normalizeErr != nil {
					violated = true
					continue
				}
				if host == d || strings.HasSuffix(host, "."+d) {
					return true
				}
			}
			return false
		}
		if (len(allowed) > 0 && !matches(allowed)) || matches(blocked) {
			violated = true
			return false
		}
		return true
	}
	add := func(rawURL, title string) {
		if !allowedURL(rawURL) {
			return
		}
		if i, exists := seen[rawURL]; exists {
			if title != "" {
				response.Results[i].Title = title
			}
			return
		}
		if len(response.Results) >= DefaultMaxResults {
			return
		}
		seen[rawURL] = len(response.Results)
		response.Results = append(response.Results, Result{URL: rawURL, Title: title})
	}
	searches := 0
	operations := 0
	var text []string
	for _, i := range indexes {
		item := items[i]
		switch item.Get("type").String() {
		case "web_search_call":
			operations++
			if target := item.Get("action.url").String(); target != "" {
				allowedURL(target)
			}
			if item.Get("status").String() != "completed" && item.Get("action.type").String() == "search" {
				return Response{}, errors.New("web search tool did not complete")
			}
			if item.Get("action.type").String() == "search" {
				searches++
			}
			for _, source := range item.Get("action.sources").Array() {
				add(source.Get("url").String(), source.Get("title").String())
			}
		case "message":
			for _, part := range item.Get("content").Array() {
				if part.Get("type").String() != "output_text" {
					continue
				}
				text = append(text, part.Get("text").String())
				for _, citation := range part.Get("annotations").Array() {
					if citation.Get("type").String() == "url_citation" {
						add(citation.Get("url").String(), citation.Get("title").String())
					}
				}
			}
		}
	}
	if operations > CallLimit(request) {
		return Response{}, ErrCallLimitExceeded
	}
	if violated {
		return Response{}, errors.New("web search returned sources outside requested domain restrictions")
	}
	if searches == 0 {
		return Response{}, errors.New("response completed without executing web search")
	}
	measured := terminal.Get("usage")
	if !measured.Exists() {
		return Response{}, errors.New("web search response is missing usage")
	}
	cached := int(measured.Get("input_tokens_details.cached_tokens").Int())
	response.Summary = strings.Join(text, "\n\n")
	response.Usage = &Usage{InputTokens: max(0, int(measured.Get("input_tokens").Int())-cached), OutputTokens: int(measured.Get("output_tokens").Int()), CacheReadInputTokens: cached, ServerToolUse: &ServerToolUsage{WebSearchRequests: searches}}
	if response.Summary == "" && len(response.Results) == 0 {
		return Response{}, errors.New("web search completed without an answer or sources")
	}
	return response, nil
}
