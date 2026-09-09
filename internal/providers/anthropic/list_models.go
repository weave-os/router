package anthropic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"

	"github.com/tidwall/gjson"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
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
	baseURL := requestcontext.EffectiveBaseURL(ctx, c.baseURL)
	if baseURL == "" {
		return nil, providers.ErrModelDiscoveryDestination
	}
	normalizedBaseURL, err := auth.NormalizeBaseURL(&baseURL)
	if err != nil || normalizedBaseURL == nil {
		return nil, providers.ErrModelDiscoveryDestination
	}
	baseURL = *normalizedBaseURL
	seen := make(map[string]struct{})
	var ids []string
	afterID := ""
	for page := 0; page < maxModelListPages; page++ {
		listURL, err := url.JoinPath(baseURL, "v1", "models")
		if err != nil {
			return nil, providers.ErrModelDiscoveryDestination
		}
		parsedListURL, err := url.Parse(listURL)
		if err != nil {
			return nil, providers.ErrModelDiscoveryDestination
		}
		query := parsedListURL.Query()
		query.Set("limit", "1000")
		if afterID != "" {
			query.Set("after_id", afterID)
		}
		parsedListURL.RawQuery = query.Encode()
		upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedListURL.String(), nil)
		if err != nil {
			return nil, providers.ErrModelDiscoveryDestination
		}
		upstream.Header.Set("anthropic-version", anthropicVersion)
		c.setAuth(ctx, upstream, upstream)
		requestcontext.ApplyWIFTokenType(ctx, upstream)
		requestcontext.ApplyIdentityHeader(ctx, upstream)

		resp, err := c.modelHTTP.Do(upstream)
		if err != nil {
			if errors.Is(err, providers.ErrModelDiscoveryDestination) {
				return nil, providers.ErrModelDiscoveryDestination
			}
			return nil, providers.ErrModelDiscoveryTransport
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && page == 0 {
			return c.listGatewayModels(ctx, baseURL)
		}
		if resp.StatusCode >= 400 {
			return nil, providers.NewModelListStatusError(resp.StatusCode)
		}
		if err != nil {
			return nil, providers.ErrModelDiscoveryTransport
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
	listURL, err := url.JoinPath(baseURL, "models")
	if err != nil {
		return nil, 0, providers.ErrModelDiscoveryDestination
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, entity)
	if err != nil {
		return nil, 0, providers.ErrModelDiscoveryDestination
	}
	if withEntity {
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Accept", "application/json")
	}
	upstream.Header.Set("anthropic-version", anthropicVersion)
	c.setAuth(ctx, upstream, upstream)
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
