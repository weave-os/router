package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"

	"github.com/tidwall/gjson"
	"workweave/router/internal/providers"
	"workweave/router/internal/proxy"
)

// maxModelListBytes caps the buffered model-list response body.
const maxModelListBytes = 1 << 20

// anthropicVersion is the required anthropic-version header value for the
// Messages API surface, including /v1/models.
const anthropicVersion = "2023-06-01"

// maxModelListPages bounds pagination so a misbehaving endpoint that always
// reports has_more can't loop forever.
const maxModelListPages = 20

// ListModels fetches GET {base}/v1/models and returns sorted, deduplicated model IDs.
// Paginates via has_more/last_id; all pages are walked up to maxModelListPages.
// Gateways that speak the Messages API without hosting /v1/models (Snowflake
// Cortex) publish their catalog at the sibling /models instead, so a 404 on the
// first page falls back to that.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	baseURL := proxy.EffectiveBaseURL(ctx, c.baseURL)
	if baseURL == "" {
		return nil, errors.New("no base URL configured for model listing")
	}
	seen := make(map[string]struct{})
	var ids []string
	afterID := ""
	for page := 0; page < maxModelListPages; page++ {
		listURL := baseURL + "/v1/models?limit=1000"
		if afterID != "" {
			listURL += "&after_id=" + url.QueryEscape(afterID)
		}
		upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build model-list request: %w", err)
		}
		upstream.Header.Set("anthropic-version", anthropicVersion)
		c.setAuth(ctx, upstream, upstream)
		proxy.ApplyWIFTokenType(ctx, upstream)
		proxy.ApplyIdentityHeader(ctx, upstream)

		resp, err := c.http.Do(upstream)
		if err != nil {
			return nil, fmt.Errorf("model-list call: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && page == 0 {
			return c.listGatewayModels(ctx, baseURL)
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("model listing returned status %d", resp.StatusCode)
		}
		if err != nil {
			return nil, fmt.Errorf("read model-list response: %w", err)
		}
		pageIDs, err := providers.ParseModelIDs(body)
		if err != nil {
			return nil, err
		}
		for _, id := range pageIDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		lastID := gjson.GetBytes(body, "last_id").String()
		if !gjson.GetBytes(body, "has_more").Bool() || lastID == "" || lastID == afterID {
			break
		}
		afterID = lastID
	}
	sort.Strings(ids)
	return ids, nil
}

// listGatewayModels reads the unpaginated catalog a Messages-API gateway serves
// at {base}/models when it hosts no /v1/models route.
func (c *Client) listGatewayModels(ctx context.Context, baseURL string) ([]string, error) {
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build model-list request: %w", err)
	}
	upstream.Header.Set("anthropic-version", anthropicVersion)
	c.setAuth(ctx, upstream, upstream)
	proxy.ApplyWIFTokenType(ctx, upstream)
	proxy.ApplyIdentityHeader(ctx, upstream)

	resp, err := c.http.Do(upstream)
	if err != nil {
		return nil, fmt.Errorf("model-list call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("model listing returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if err != nil {
		return nil, fmt.Errorf("read model-list response: %w", err)
	}
	return providers.ParseModelIDs(body)
}

var _ providers.ModelLister = (*Client)(nil)
