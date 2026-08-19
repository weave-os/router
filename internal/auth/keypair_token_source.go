package auth

import (
	"time"

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
