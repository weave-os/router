// Package codex is the credential-scoped Provider adapter for ChatGPT/Codex
// subscription OAuth. The wire implementation is shared with the OpenAI
// Responses client, but registration stays separate so routing cannot confuse
// a subscription bearer with an OpenAI API key.
package codex

import (
	"context"
	"fmt"
	"net/http"

	"workweave/router/internal/providers"
	openaiProvider "workweave/router/internal/providers/openai"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
)

// Client forwards only native Responses requests authenticated by a resolved
// Codex OAuth credential. It deliberately has no deployment API key.
type Client struct {
	delegate *openaiProvider.Client
}

// NewClient constructs a Codex adapter. baseURL is intended for self-hosted
// tests and development; production uses the adapter's ChatGPT Codex default.
func NewClient(baseURL string) *Client {
	delegate := openaiProvider.NewClient("", openaiProvider.DefaultBaseURL)
	if baseURL != "" {
		delegate.SetCodexBaseURL(baseURL)
	}
	return &Client{delegate: delegate}
}

// Proxy forwards a native Responses request to the Codex backend after
// requiring the request-scoped OAuth token and account-id pair.
func (c *Client) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	creds := proxy.CredentialsFromContext(ctx)
	if creds == nil || !creds.OAuth || len(creds.APIKey) == 0 || len(creds.AccountID) == 0 {
		return fmt.Errorf("codex provider requires a Codex OAuth credential and account ID")
	}
	if prep.Endpoint != providers.EndpointResponses {
		return fmt.Errorf("codex provider requires the Responses endpoint")
	}
	return c.delegate.Proxy(ctx, decision, prep, w, r)
}

// Passthrough is unsupported because Codex credentials are valid only for the
// routed native Responses dispatch, never an arbitrary client path.
func (c *Client) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

var _ providers.Client = (*Client)(nil)
