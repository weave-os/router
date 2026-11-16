package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/httputil"
)

const codexImagesResponseHeaderTimeout = 300 * time.Second

var codexImageForwardedHeaders = []string{"Content-Type", "Accept", "X-Codex-Image-Turn-Id", "X-Codex-Turn-Metadata"}

func (c *Client) ForwardCodexImageRequest(ctx context.Context, operation string, body []byte, inbound http.Header, w http.ResponseWriter) error {
	creds := codexSubscriptionCreds(ctx)
	if creds == nil {
		return errors.New("codex image requests require ChatGPT OAuth and account ID")
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, c.codexBaseURL+"/images/"+operation, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build Codex image request: %w", err)
	}
	for _, name := range codexImageForwardedHeaders {
		if v := inbound.Get(name); v != "" {
			upstream.Header.Set(name, v)
		}
	}
	upstream.Header.Set("Authorization", "Bearer "+string(creds.APIKey))
	upstream.Header.Set(codexAccountIDHeader, string(creds.AccountID))
	upstream.Header.Set(codexOriginatorHeader, codexOriginatorValue)
	upstream.Header.Set(codexUserAgentHeader, codexUserAgentValue)
	resp, err := c.imagesHTTP.Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream Codex image call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 {
		return httputil.WritePassthroughError(ctx, w, resp, nil, nil, "Upstream Codex images returned error status", "operation", operation)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
