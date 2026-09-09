package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

// maxModelListBytes caps the buffered model-list response body.
const maxModelListBytes = 1 << 20

// ListModels returns the model IDs the endpoint publishes, sorted and
// deduplicated, walking modelListURLs until one answers something other than a
// 404.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	baseURL := c.effectiveBaseURL(ctx)
	if baseURL == "" {
		return nil, providers.ErrModelDiscoveryDestination
	}
	normalizedBaseURL, err := auth.NormalizeBaseURL(&baseURL)
	if err != nil || normalizedBaseURL == nil {
		return nil, providers.ErrModelDiscoveryDestination
	}
	baseURL = *normalizedBaseURL
	listURLs, err := modelListURLs(baseURL)
	if err != nil {
		return nil, providers.ErrModelDiscoveryDestination
	}
	var (
		ids     []string
		listErr error
	)
	for _, listURL := range listURLs {
		var status int
		ids, status, listErr = c.listModelsAt(ctx, listURL)
		if status != http.StatusNotFound {
			return ids, listErr
		}
	}
	return ids, listErr
}

// modelListURLs returns catalog URLs to try for baseURL, likeliest first.
// For gateways that mount their catalog above /v1 (e.g. Snowflake Cortex:
// /api/v2/cortex/models vs /api/v2/cortex/v1/chat/completions), also
// includes one segment up.
func modelListURLs(baseURL string) ([]string, error) {
	primary, err := url.JoinPath(baseURL, "models")
	if err != nil {
		return nil, err
	}
	urls := []string{primary}
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(parsedBaseURL.Path, "/v1") {
		parsedBaseURL.Path = strings.TrimSuffix(parsedBaseURL.Path, "/v1")
		parsedBaseURL.RawPath = ""
		fallback, joinErr := url.JoinPath(parsedBaseURL.String(), "models")
		if joinErr != nil {
			return nil, joinErr
		}
		urls = append(urls, fallback)
	}
	return urls, nil
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
		return nil, 0, providers.ErrModelDiscoveryDestination
	}
	if withEntity {
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Accept", "application/json")
	}
	c.setAuth(ctx, upstream)
	requestcontext.ApplyWIFTokenType(ctx, upstream)
	requestcontext.ApplyIdentityHeader(ctx, upstream)

	resp, err := c.modelHTTP.Do(upstream)
	if err != nil {
		if errors.Is(err, providers.ErrModelDiscoveryDestination) {
			return nil, 0, providers.ErrModelDiscoveryDestination
		}
		return nil, 0, providers.ErrModelDiscoveryTransport
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, providers.NewModelListStatusError(resp.StatusCode)
	}
	if err != nil {
		return nil, resp.StatusCode, providers.ErrModelDiscoveryTransport
	}
	ids, err := providers.ParseModelIDs(body)
	return ids, resp.StatusCode, err
}

var _ providers.ModelLister = (*Client)(nil)
