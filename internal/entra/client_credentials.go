// Package entra provides Microsoft Entra client-credentials token sources.
package entra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"weave-os/router/internal/auth"
)

const (
	cacheSize       = 1024
	refreshFraction = 0.8
	maxErrorBody    = 4 * 1024
	mintTimeout     = 30 * time.Second
)

type cachedToken struct {
	token     []byte
	refreshAt time.Time
}

// ClientCredentialsSource obtains Microsoft Entra access tokens using an
// external API key's tenant, client ID, and client secret.
type ClientCredentialsSource struct {
	httpClient *http.Client
	now        auth.Clock

	mu    sync.Mutex
	cache *lru.Cache[string, cachedToken]
	group singleflight.Group
}

// NewClientCredentialsSource constructs a source with the supplied HTTP client
// and clock. A nil client uses http.DefaultClient; a nil clock uses time.Now.
func NewClientCredentialsSource(httpClient *http.Client, now auth.Clock) *ClientCredentialsSource {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	cache, err := lru.New[string, cachedToken](cacheSize)
	if err != nil {
		panic("entra: invalid token cache size")
	}
	return &ClientCredentialsSource{
		httpClient: httpClient,
		now:        now,
		cache:      cache,
	}
}

// Token returns a cached or freshly minted Microsoft Entra access token for key.
func (s *ClientCredentialsSource) Token(ctx context.Context, key *auth.ExternalAPIKey) ([]byte, error) {
	if key == nil {
		return nil, fmt.Errorf("%w: key is nil", auth.ErrEntraUnavailable)
	}
	if strings.TrimSpace(key.AuthAccount) == "" {
		return nil, fmt.Errorf("%w: tenant ID is missing", auth.ErrEntraUnavailable)
	}
	if strings.TrimSpace(key.AuthUser) == "" {
		return nil, fmt.Errorf("%w: client ID is missing", auth.ErrEntraUnavailable)
	}
	if len(key.Plaintext) == 0 {
		return nil, fmt.Errorf("%w: client secret is missing", auth.ErrEntraUnavailable)
	}

	cacheKey := key.ID + "\x00" + key.KeyFingerprint
	now := s.now()
	s.mu.Lock()
	cached, ok := s.cache.Get(cacheKey)
	s.mu.Unlock()
	if ok && now.Before(cached.refreshAt) {
		return append([]byte(nil), cached.token...), nil
	}

	// The shared mint runs under a detached context so a cancelling caller
	// doesn't abort the flight for waiters whose own contexts are still live.
	result := s.group.DoChan(cacheKey, func() (any, error) {
		mintCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mintTimeout)
		defer cancel()
		return s.mint(mintCtx, key, cacheKey)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-result:
		if response.Err != nil {
			return nil, response.Err
		}
		token, ok := response.Val.([]byte)
		if !ok {
			return nil, fmt.Errorf("%w: token source returned an invalid value", auth.ErrEntraUnavailable)
		}
		return append([]byte(nil), token...), nil
	}
}

func (s *ClientCredentialsSource) mint(ctx context.Context, key *auth.ExternalAPIKey, cacheKey string) ([]byte, error) {
	now := s.now()
	s.mu.Lock()
	cached, ok := s.cache.Get(cacheKey)
	s.mu.Unlock()
	if ok && now.Before(cached.refreshAt) {
		return append([]byte(nil), cached.token...), nil
	}

	tenant := strings.TrimSpace(key.AuthAccount)
	clientID := strings.TrimSpace(key.AuthUser)
	endpoint := "https://login.microsoftonline.com/" + url.PathEscape(tenant) + "/oauth2/v2.0/token"
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {string(key.Plaintext)},
		"scope":         {auth.EntraScopeForBaseURL(key.BaseURL)},
		"grant_type":    {"client_credentials"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: build token request: %v", auth.ErrEntraUnavailable, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: request token: %v", auth.ErrEntraUnavailable, err)
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	if readErr != nil {
		return nil, fmt.Errorf("%w: read token response: %v", auth.ErrEntraUnavailable, readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: token endpoint returned status %d", auth.ErrEntraUnavailable, response.StatusCode)
	}

	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return nil, fmt.Errorf("%w: decode token response: %v", auth.ErrEntraUnavailable, err)
	}
	if tokenResponse.AccessToken == "" || tokenResponse.ExpiresIn <= 0 {
		return nil, fmt.Errorf("%w: token response is missing access_token or expires_in", auth.ErrEntraUnavailable)
	}

	refreshAfter := time.Duration(float64(tokenResponse.ExpiresIn)*refreshFraction) * time.Second
	if refreshAfter <= 0 {
		return nil, fmt.Errorf("%w: token expiry is invalid (%s)", auth.ErrEntraUnavailable, strconv.FormatInt(tokenResponse.ExpiresIn, 10))
	}
	token := []byte(tokenResponse.AccessToken)
	s.mu.Lock()
	s.cache.Add(cacheKey, cachedToken{token: token, refreshAt: now.Add(refreshAfter)})
	s.mu.Unlock()
	return append([]byte(nil), token...), nil
}

var _ auth.EntraTokenSource = (*ClientCredentialsSource)(nil)
