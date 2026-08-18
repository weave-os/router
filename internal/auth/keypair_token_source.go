package auth

import (
	"time"

	"workweave/router/internal/observability"

	lru "github.com/hashicorp/golang-lru/v2"
)

// keypairTokenReuse is how long a minted token is served from cache. Shorter
// than KeypairTokenTTL so a request never leaves with a token about to expire.
const keypairTokenReuse = 45 * time.Minute

// keypairTokenCacheSize bounds the per-installation minted tokens held in memory.
const keypairTokenCacheSize = 1024

// cachedKeypairToken is a minted token with the secret it was signed from.
// Rotating a key changes the fingerprint, which invalidates the entry before
// the old token's own expiry.
type cachedKeypairToken struct {
	token       []byte
	fingerprint string
	expiresAt   time.Time
}

// KeypairTokenCache mints and reuses short-lived key-pair JWTs, one per external
// API key. Signing is pure CPU, so the cache exists to bound RSA work per
// request, not to hide I/O.
type KeypairTokenCache struct {
	now   Clock
	cache *lru.Cache[string, cachedKeypairToken]
}

// NewKeypairTokenCache constructs a KeypairTokenCache reading time from now.
func NewKeypairTokenCache(now Clock) *KeypairTokenCache {
	cache, err := lru.New[string, cachedKeypairToken](keypairTokenCacheSize)
	if err != nil {
		// Only reachable with a non-positive size, which is a constant here.
		panic("auth: invalid keypair token cache size")
	}
	return &KeypairTokenCache{now: now, cache: cache}
}

// Bearer returns the credential to send upstream for key: the stored secret for
// bearer keys, or a minted (possibly cached) JWT for key-pair keys.
func (c *KeypairTokenCache) Bearer(key *ExternalAPIKey) ([]byte, error) {
	if key.AuthType != AuthTypeKeypairJWT {
		return key.Plaintext, nil
	}
	now := c.now()
	if entry, ok := c.cache.Get(key.ID); ok &&
		entry.fingerprint == key.KeyFingerprint && now.Before(entry.expiresAt) {
		return entry.token, nil
	}
	priv, err := ParseKeypairPrivateKey(key.Plaintext)
	if err != nil {
		return nil, err
	}
	token, err := MintKeypairJWT(priv, key.AuthAccount, key.AuthUser, now, KeypairTokenTTL)
	if err != nil {
		return nil, err
	}
	minted := []byte(token)
	c.cache.Add(key.ID, cachedKeypairToken{
		token:       minted,
		fingerprint: key.KeyFingerprint,
		expiresAt:   now.Add(keypairTokenReuse),
	})
	return minted, nil
}

// resolveUpstreamSecrets replaces each key's Plaintext with its upstream credential, minting
// JWTs as needed. Keys that fail to mint are dropped — never sent as raw private keys.
func (s *Service) resolveUpstreamSecrets(keys []*ExternalAPIKey) []*ExternalAPIKey {
	if !anyKeypairAuth(keys) {
		return keys
	}
	resolved := make([]*ExternalAPIKey, 0, len(keys))
	for _, key := range keys {
		bearer, err := s.keypairTokens.Bearer(key)
		if err != nil {
			observability.Get().Warn("Failed to mint keypair token for external API key",
				"external_api_key_id", key.ID, "provider", key.Provider, "err", err)
			continue
		}
		if key.AuthType != AuthTypeKeypairJWT {
			resolved = append(resolved, key)
			continue
		}
		// Copy so the minted token never overwrites the private key held by the
		// shared cache entry these keys came from.
		withToken := *key
		withToken.Plaintext = bearer
		resolved = append(resolved, &withToken)
	}
	return resolved
}

// anyKeypairAuth reports whether any key needs a minted token.
func anyKeypairAuth(keys []*ExternalAPIKey) bool {
	for _, key := range keys {
		if key.AuthType == AuthTypeKeypairJWT {
			return true
		}
	}
	return false
}
