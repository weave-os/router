package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const maxCodexModelCatalogBytes = 1 << 20

// FetchCodexModelCatalog reads the caller's account-specific Codex catalog.
// Only the paired ChatGPT OAuth credential may go to the Codex backend.
func (c *Client) FetchCodexModelCatalog(ctx context.Context, clientVersion string) ([]byte, error) {
	creds := codexSubscriptionCreds(ctx)
	if creds == nil {
		return nil, errors.New("Codex model discovery requires ChatGPT OAuth and account ID")
	}
	endpoint, err := url.Parse(c.codexBaseURL + "/models")
	if err != nil {
		return nil, fmt.Errorf("parse Codex models endpoint: %w", err)
	}
	query := endpoint.Query()
	if clientVersion != "" {
		query.Set("client_version", clientVersion)
	}
	endpoint.RawQuery = query.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build Codex models request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+string(creds.APIKey))
	request.Header.Set(codexAccountIDHeader, string(creds.AccountID))
	request.Header.Set(codexOriginatorHeader, codexOriginatorValue)
	request.Header.Set(codexUserAgentHeader, codexUserAgentValue)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch Codex models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Codex models endpoint returned HTTP %d", response.StatusCode)
	}
	catalog, err := io.ReadAll(io.LimitReader(response.Body, maxCodexModelCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Codex models: %w", err)
	}
	if len(catalog) > maxCodexModelCatalogBytes {
		return nil, fmt.Errorf("Codex model catalog exceeds %d bytes", maxCodexModelCatalogBytes)
	}
	return catalog, nil
}
