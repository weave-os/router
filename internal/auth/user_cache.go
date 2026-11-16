package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

type UserCache interface {
	Get(installationID, identityKey string) (string, bool)
	Set(installationID, identityKey, userID string)
}

type NoOpUserCache struct{}

func (NoOpUserCache) Get(string, string) (string, bool) { return "", false }
func (NoOpUserCache) Set(string, string, string)        {}

type LRUUserCache struct {
	store *expirable.LRU[string, string]
}

func NewLRUUserCache(size int, ttl time.Duration) *LRUUserCache {
	return &LRUUserCache{
		store: expirable.NewLRU[string, string](size, nil, ttl),
	}
}

func (c *LRUUserCache) Get(installationID, identityKey string) (string, bool) {
	return c.store.Get(userCacheKey(installationID, identityKey))
}

func (c *LRUUserCache) Set(installationID, identityKey, userID string) {
	c.store.Add(userCacheKey(installationID, identityKey), userID)
}

func userCacheKey(installationID, identityKey string) string {
	h := sha256.New()
	h.Write([]byte(installationID))
	h.Write([]byte{0x00})
	h.Write([]byte(identityKey))
	return hex.EncodeToString(h.Sum(nil))
}
