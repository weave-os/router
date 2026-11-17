package auth

import (
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

const keypairTokenReuse = 45 * time.Minute

const keypairTokenCacheSize = 1024

type cachedKeypairToken struct {
	token       []byte
	fingerprint string
	expiresAt   time.Time
}

type KeypairTokenCache struct {
	now   Clock
	cache *lru.Cache[string, cachedKeypairToken]
}

func NewKeypairTokenCache(now Clock) *KeypairTokenCache {
	cache, err := lru.New[string, cachedKeypairToken](keypairTokenCacheSize)
	if err != nil {
		panic("auth: invalid keypair token cache size")
	}
	return &KeypairTokenCache{now: now, cache: cache}
}

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
