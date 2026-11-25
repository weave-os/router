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
	"weave-os/router/internal/requestcontext"

	"github.com/tidwall/gjson"
)

const codexImagesResponseHeaderTimeout = 300 * time.Second

var codexImageForwardedHeaders = []string{"Content-Type", "Accept", "X-Codex-Image-Turn-Id", "X-Codex-Turn-Metadata"}

func (c *Client) ForwardCodexImageRequest(ctx context.Context, operation string, body []byte, inbound http.Header, w http.ResponseWriter) error {
	upstream, err := c.codexImageUpstream(ctx, operation, body)
	if err != nil {
		return err
	}
	for _, name := range codexImageForwardedHeaders {
		if v := inbound.Get(name); v != "" {
			upstream.Header.Set(name, v)
		}
	}
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

func (c *Client) codexImageUpstream(ctx context.Context, operation string, body []byte) (*http.Request, error) {
	creds := requestcontext.CredentialsFromContext(ctx)
	if creds == nil || len(creds.APIKey) == 0 {
		return nil, errors.New("codex image requests require the caller's own OpenAI credential")
	}
	subscription := codexSubscriptionCreds(ctx) != nil
	url := c.baseURL + "/v1/images/" + operation
	if subscription {
		if err := c.checkCodexIncludedQuota(ctx, gjson.GetBytes(body, "model").String(), c.allowCodexExtraCredits); err != nil {
			return nil, err
		}
		url = c.codexBaseURL + "/images/" + operation
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build Codex image request: %w", err)
	}
	upstream.Header.Set("Authorization", "Bearer "+string(creds.APIKey))
	if subscription {
		upstream.Header.Set(codexAccountIDHeader, string(creds.AccountID))
		upstream.Header.Set(codexOriginatorHeader, codexOriginatorValue)
		upstream.Header.Set(codexUserAgentHeader, codexUserAgentValue)
	}
	return upstream, nil
}
