package anthropic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"

	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
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
			return nil, providers.ModelListStatusError(resp.StatusCode, body)
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
// at {base}/models when it hosts no /v1/models route. A gateway that demands an
// entity on that GET is retried once with an empty JSON body.
func (c *Client) listGatewayModels(ctx context.Context, baseURL string) ([]string, error) {
	ids, status, err := c.getGatewayModels(ctx, baseURL, false)
	if !providers.ModelListNeedsEntity(status) {
		return ids, err
	}
	ids, _, err = c.getGatewayModels(ctx, baseURL, true)
	return ids, err
}

func (c *Client) getGatewayModels(ctx context.Context, baseURL string, withEntity bool) ([]string, int, error) {
	var entity io.Reader
	if withEntity {
		entity = bytes.NewReader(providers.EmptyJSONEntity)
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", entity)
	if err != nil {
		return nil, 0, fmt.Errorf("build model-list request: %w", err)
	}
	if withEntity {
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Accept", "application/json")
	}
	upstream.Header.Set("anthropic-version", anthropicVersion)
	c.setAuth(ctx, upstream, upstream)
	proxy.ApplyWIFTokenType(ctx, upstream)
	proxy.ApplyIdentityHeader(ctx, upstream)

	resp, err := c.http.Do(upstream)
	if err != nil {
		return nil, 0, fmt.Errorf("model-list call: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, providers.ModelListStatusError(resp.StatusCode, body)
	}
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read model-list response: %w", err)
	}
	ids, err := providers.ParseModelIDs(body)
	return ids, resp.StatusCode, err
}

var _ providers.ModelLister = (*Client)(nil)
