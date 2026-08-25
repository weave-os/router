package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"workweave/router/internal/providers"
	"workweave/router/internal/proxy"
)

// maxModelListBytes caps the buffered model-list response body.
const maxModelListBytes = 1 << 20

// ListModels fetches GET {base}/models and returns the model IDs the endpoint
// publishes, sorted and deduplicated. Gateways that mount their chat surface
// under /v1 but their catalog above it (Snowflake Cortex serves
// /api/v2/cortex/models next to /api/v2/cortex/v1/chat/completions) are retried
// one path segment up on a 404.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	baseURL := proxy.EffectiveBaseURL(ctx, c.baseURL)
	if baseURL == "" {
		return nil, errors.New("no base URL configured for model listing")
	}
	ids, status, err := c.listModelsAt(ctx, baseURL+"/models")
	if status != http.StatusNotFound {
		return ids, err
	}
	root, trimmed := strings.CutSuffix(baseURL, "/v1")
	if !trimmed {
		return ids, err
	}
	ids, _, err = c.listModelsAt(ctx, root+"/models")
	return ids, err
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
