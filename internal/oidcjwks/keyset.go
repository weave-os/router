package oidcjwks

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/sync/singleflight"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

const (
	DefaultTTL         = 10 * time.Minute
	StaleKeyGrace      = 24 * time.Hour
	minRefreshInterval = time.Minute
	maxDocumentBytes   = 1 << 20
	fetchTimeout       = 10 * time.Second
	discoveryPath      = "/.well-known/openid-configuration"
)

var ErrFetch = errors.New("oidcjwks: issuer key fetch failed")

var errUntrustedDiscovery = fmt.Errorf("%w: untrusted issuer discovery", ErrFetch)

type cachedSet struct {
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time
}

type refreshFailure struct {
	at  time.Time
	err error
}

type KeySet struct {
	httpClient *http.Client
	now        auth.Clock
	ttl        time.Duration

	mu       sync.Mutex
	sets     map[string]cachedSet
	failures map[string]refreshFailure
	group    singleflight.Group
}

func New(httpClient *http.Client, now auth.Clock) *KeySet {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: fetchTimeout}
	}
	if now == nil {
		now = time.Now
	}
	return &KeySet{httpClient: observability.WrapHTTPClient(httpClient), now: now, ttl: DefaultTTL, sets: map[string]cachedSet{}, failures: map[string]refreshFailure{}}
}

func (k *KeySet) VerificationKey(ctx context.Context, issuerURL, kid string) (crypto.PublicKey, error) {
	set, ok := k.cached(issuerURL)
	if ok && k.now().Sub(set.fetchedAt) < k.ttl {
		if key, found := set.keys[kid]; found {
			return key, nil
		}
		if k.now().Sub(set.fetchedAt) < minRefreshInterval {
			return nil, auth.ErrInvalidToken
		}
	}
	if failure, failed := k.recentFailure(issuerURL); failed {
		return k.lastGoodKey(set, ok, kid, failure)
	}
	refreshed, err := k.refresh(ctx, issuerURL)
	if err != nil {
		key, staleErr := k.lastGoodKey(set, ok, kid, err)
		if staleErr == nil {
			observability.FromContext(ctx).Warn("OIDC issuer key refresh failed; using last-good keys",
				"issuer", issuerURL, "keys_age", k.now().Sub(set.fetchedAt).String(), "err", err)
		}
		return key, staleErr
	}
	if key, found := refreshed.keys[kid]; found {
		return key, nil
	}
	return nil, auth.ErrInvalidToken
}

func (k *KeySet) lastGoodKey(set cachedSet, ok bool, kid string, refreshErr error) (crypto.PublicKey, error) {
	if ok && !errors.Is(refreshErr, errUntrustedDiscovery) && k.now().Sub(set.fetchedAt) < StaleKeyGrace {
		if key, found := set.keys[kid]; found {
			return key, nil
		}
	}
	return nil, refreshErr
}

func (k *KeySet) cached(issuerURL string) (cachedSet, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	set, ok := k.sets[issuerURL]
	return set, ok
}

func (k *KeySet) recentFailure(issuerURL string) (error, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	failure, ok := k.failures[issuerURL]
	if !ok || k.now().Sub(failure.at) >= minRefreshInterval {
		return nil, false
	}
	return failure.err, true
}

func (k *KeySet) refresh(ctx context.Context, issuerURL string) (cachedSet, error) {
	v, err, _ := k.group.Do(issuerURL, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()
		keys, err := k.fetch(fetchCtx, issuerURL)
		k.mu.Lock()
		defer k.mu.Unlock()
		if err != nil {
			k.failures[issuerURL] = refreshFailure{at: k.now(), err: err}
			return cachedSet{}, err
		}
		delete(k.failures, issuerURL)
		set := cachedSet{keys: keys, fetchedAt: k.now()}
		k.sets[issuerURL] = set
		return set, nil
	})
	if err != nil {
		return cachedSet{}, err
	}
	set, ok := v.(cachedSet)
	if !ok {
		return cachedSet{}, ErrFetch
	}
	return set, nil
}

func (k *KeySet) fetch(ctx context.Context, issuerURL string) (map[string]crypto.PublicKey, error) {
	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := k.getJSON(ctx, strings.TrimSuffix(issuerURL, "/")+discoveryPath, &discovery); err != nil {
		return nil, err
	}
	if discovery.Issuer != issuerURL {
		return nil, fmt.Errorf("%w: discovery issuer %q does not match %q", errUntrustedDiscovery, discovery.Issuer, issuerURL)
	}
	if parsed, err := url.Parse(discovery.JWKSURI); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: jwks_uri must be an https URL", errUntrustedDiscovery)
	}
	var set jose.JSONWebKeySet
	if err := k.getJSON(ctx, discovery.JWKSURI, &set); err != nil {
		return nil, err
	}
	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	for _, key := range set.Keys {
		if key.KeyID == "" || key.Use == "enc" || !key.IsPublic() || !key.Valid() {
			continue
		}
		keys[key.KeyID] = key.Key
	}
	return keys, nil
}

func (k *KeySet) getJSON(ctx context.Context, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFetch, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := k.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFetch, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", ErrFetch, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDocumentBytes)).Decode(out); err != nil {
		return fmt.Errorf("%w: %w", ErrFetch, err)
	}
	return nil
}
