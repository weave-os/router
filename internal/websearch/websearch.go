// Package websearch models the Anthropic native web-search server tool for
// upstreams that cannot execute it themselves. Pure: detection and response
// synthesis have no I/O; execution is an injected Executor.
package websearch

import (
	"context"
	"errors"

	"github.com/tidwall/gjson"
)

// DefaultMaxResults caps how many results an executor returns for one search.
const DefaultMaxResults = 10

// Query is a single web search to execute.
type Query struct {
	// Text is the search query as the client asked for it.
	Text string
	// MaxResults bounds the result count; zero means DefaultMaxResults.
	MaxResults int
}

// Result is one search hit, shaped for Anthropic's web_search_result block.
type Result struct {
	Title   string
	URL     string
	Snippet string
	// PageAge is the upstream's freshness string (e.g. "2025-08-01"); empty
	// when the backend does not report it.
	PageAge string
}

// Response is an executed search: the hits plus any answer text the backend
// synthesized while searching.
type Response struct {
	// Model identifies the upstream model that performed the search.
	Model   string
	Usage   *Usage
	Results []Result
	Summary string
}

// Executor runs a web search on a backend the tenant is already routed to.
// Implementations are provider adapters and read per-request credentials from
// ctx, exactly like a providers.Client.
type Executor interface {
	Search(ctx context.Context, q Query) (Response, error)
}

// ErrCallLimitExceeded reports an explicit web-search operation budget stop.
var ErrCallLimitExceeded = errors.New("web search max_uses exceeded")

// MaxNativeSearchOperations is the bridge's server-side native tool budget.
const MaxNativeSearchOperations = 10

// CallLimit bounds native tool operations for an isolated search helper.
func CallLimit(body []byte) int {
	if value := gjson.GetBytes(body, "tools.0.max_uses").Int(); value > 0 {
		if value > MaxNativeSearchOperations {
			return MaxNativeSearchOperations
		}
		return int(value)
	}
	return 1
}
