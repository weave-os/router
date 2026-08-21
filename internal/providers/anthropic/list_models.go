package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// ListModels fetches GET {base}/v1/models and returns the model IDs the
// endpoint publishes ({"data":[{"id":...}]}), sorted and deduplicated.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	baseURL := proxy.EffectiveBaseURL(ctx, c.baseURL)
	if baseURL == "" {
		return nil, errors.New("no base URL configured for model listing")
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
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
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() {
		return nil, errors.New("model listing response has no data array")
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, entry := range data.Array() {
		id := entry.Get("id").String()
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

var _ providers.ModelLister = (*Client)(nil)
