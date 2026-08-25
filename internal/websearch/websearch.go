// Package websearch models the Anthropic native web-search server tool for
// upstreams that cannot execute it themselves. Pure: detection and response
// synthesis have no I/O; execution is an injected Executor.
package websearch

import "context"

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
	Results []Result
	Summary string
}

// Executor runs a web search on a backend the tenant is already routed to.
// Implementations are provider adapters and read per-request credentials from
// ctx, exactly like a providers.Client.
type Executor interface {
	Search(ctx context.Context, q Query) (Response, error)
}
