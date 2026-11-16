package openaicompat

import (
	"context"
	"weave-os/router/internal/providers/httputil"
)

func (c *Client) WarmTransport(ctx context.Context, requiredOrigin string) (bool, error) {
	return httputil.WarmTransport(ctx, c.baseURL, requiredOrigin, c.http, c.grokHTTP)
}
