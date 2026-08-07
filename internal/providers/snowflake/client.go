// Package snowflake adapts the Anthropic Messages client for Snowflake Cortex
// REST. Cortex's Messages surface follows the Anthropic spec; the differences
// from api.anthropic.com are Bearer-scheme auth (a Snowflake PAT) and a
// per-account base URL with no deployment-wide default.
package snowflake

import (
	"strings"

	"workweave/router/internal/providers"
	"workweave/router/internal/providers/anthropic"
)

// CortexPathPrefix is the path Cortex's REST surfaces hang off. The Messages
// endpoint is CortexPathPrefix + "/v1/messages".
const CortexPathPrefix = "/api/v2/cortex"

// Client is the Cortex-configured Anthropic Messages adapter.
type Client struct {
	*anthropic.Client
}

// NewClient returns a Cortex client. token is the deployment-level PAT (empty
// in BYOK-only mode, where the per-installation key supplies both token and
// base URL); baseURL is the account endpoint, with or without CortexPathPrefix.
func NewClient(token, baseURL string) *Client {
	return &Client{Client: anthropic.NewClient(
		token,
		NormalizeBaseURL(baseURL),
		anthropic.WithAuthScheme(anthropic.AuthBearer),
		anthropic.WithBaseURLRewriter(NormalizeBaseURL),
	)}
}

// NormalizeBaseURL appends CortexPathPrefix when the configured endpoint is a
// bare account URL (https://<account>.snowflakecomputing.com), so an operator
// pasting either form reaches the same Messages endpoint. An empty base URL
// stays empty — there is no sensible Snowflake default to fall back to.
func NormalizeBaseURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" || strings.HasSuffix(trimmed, CortexPathPrefix) {
		return trimmed
	}
	return trimmed + CortexPathPrefix
}

var _ providers.Client = (*Client)(nil)
