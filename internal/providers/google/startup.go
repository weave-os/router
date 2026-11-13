package google

import (
	"context"
	"weave-os/router/internal/providers/httputil"
)

// WarmTransport initializes the configured origin using the serving transports.
func (c *NativeClient) WarmTransport(ctx context.Context, requiredOrigin string) (bool, error) {
	return httputil.WarmTransport(ctx, c.baseURL, requiredOrigin, c.http)
}
