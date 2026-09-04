package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
)

// maxModelListBytes caps the buffered model-list response body.
const maxModelListBytes = 1 << 20

// ListModels returns the model IDs the endpoint publishes, sorted and
// deduplicated, walking modelListURLs until one answers something other than a
// 404.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	baseURL := c.effectiveBaseURL(ctx)
	if baseURL == "" {
		return nil, errors.New("no base URL configured for model listing")
	}
	var (
		ids []string
		err error
	)
	for _, listURL := range modelListURLs(baseURL) {
		var status int
		ids, status, err = c.listModelsAt(ctx, listURL)
		if status != http.StatusNotFound {
			return ids, err
		}
	}
	return ids, err
}

// modelListURLs returns catalog URLs to try for baseURL, likeliest first.
// For gateways that mount their catalog above /v1 (e.g. Snowflake Cortex:
// /api/v2/cortex/models vs /api/v2/cortex/v1/chat/completions), also
// includes one segment up.
func modelListURLs(baseURL string) []string {
	urls := []string{baseURL + "/models"}
	if root, trimmed := strings.CutSuffix(baseURL, "/v1"); trimmed {
		urls = append(urls, root+"/models")
	}
	return urls
}

// listModelsAt reads one model-list URL, also reporting the upstream status so
// the caller can decide whether another path is worth trying. A gateway that
// demands an entity on the catalog GET is retried once with an empty JSON body.
func (c *Client) listModelsAt(ctx context.Context, listURL string) ([]string, int, error) {
	ids, status, err := c.getModelList(ctx, listURL, false)
	if !providers.ModelListNeedsEntity(status) {
		return ids, status, err
	}
	return c.getModelList(ctx, listURL, true)
}

func (c *Client) getModelList(ctx context.Context, listURL string, withEntity bool) ([]string, int, error) {
	var entity io.Reader
	if withEntity {
		entity = bytes.NewReader(providers.EmptyJSONEntity)
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, entity)
	if err != nil {
		return nil, 0, fmt.Errorf("build model-list request: %w", err)
	}
	if withEntity {
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Accept", "application/json")
	}
	c.setAuth(ctx, upstream)
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
