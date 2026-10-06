package policyregistry

import (
	"context"
	"errors"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"
)

type admissionCacheKey struct{ Installation, APIKey, Session string }
type admissionCacheEntry struct {
	Scope   AdmissionScope
	Binding SessionReleaseBinding
	Expires time.Time
}

// AdmissionDecisionCache keeps primary admission authoritative on misses. Its
// epoch prevents a transaction racing invalidation from repopulating stale state.
type AdmissionDecisionCache struct {
	store   ServingAdmissionStore
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	epoch   uint64
	entries *lru.Cache[admissionCacheKey, admissionCacheEntry]
	flights singleflight.Group
}

func NewAdmissionDecisionCache(store ServingAdmissionStore, capacity int, ttl time.Duration, now func() time.Time) (*AdmissionDecisionCache, error) {
	if store == nil || ttl <= 0 || ttl > time.Minute || now == nil {
		return nil, errors.New("admission cache requires a store, clock and TTL at most one minute")
	}
	entries, err := lru.New[admissionCacheKey, admissionCacheEntry](capacity)
	if err != nil {
		return nil, err
	}
	return &AdmissionDecisionCache{store: store, ttl: ttl, now: now, entries: entries}, nil
}

func (c *AdmissionDecisionCache) lookup(key admissionCacheKey) (admissionCacheEntry, bool) {
	entry, ok := c.entries.Get(key)
	if ok && !c.now().Before(entry.Expires) {
		c.entries.Remove(key)
		return admissionCacheEntry{}, false
	}
	return entry, ok
}

func (c *AdmissionDecisionCache) Admit(ctx context.Context, installationID, apiKeyID, sessionID string, decide AdmissionDecision) (AdmissionScope, SessionReleaseBinding, error) {
	// Anonymous turns must select the current release independently.
	if sessionID == "" {
		return c.store.Admit(ctx, installationID, apiKeyID, sessionID, decide)
	}
	key := admissionCacheKey{installationID, apiKeyID, Digest([]byte(sessionID))}
	c.mu.Lock()
	if entry, ok := c.lookup(key); ok {
		c.mu.Unlock()
		return entry.Scope, entry.Binding, nil
	}
	epoch := c.epoch
	c.mu.Unlock()
	encoded, err := CanonicalBytes(struct {
		Key   admissionCacheKey
		Epoch uint64
	}{key, epoch})
	if err != nil {
		return AdmissionScope{}, SessionReleaseBinding{}, err
	}
	result := c.flights.DoChan(string(encoded), func() (any, error) {
		c.mu.Lock()
		if entry, ok := c.lookup(key); ok {
			c.mu.Unlock()
			return entry, nil
		}
		c.mu.Unlock()
		// A caller leaving must not cancel the shared binding transaction.
		admissionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		scope, binding, err := c.store.Admit(admissionCtx, installationID, apiKeyID, sessionID, decide)
		if err != nil {
			return nil, err
		}
		entry := admissionCacheEntry{Scope: scope, Binding: binding, Expires: c.now().Add(c.ttl)}
		c.mu.Lock()
		if c.epoch == epoch && scope.Persistent {
			c.entries.Add(key, entry)
		}
		c.mu.Unlock()
		return entry, nil
	})
	select {
	case <-ctx.Done():
		return AdmissionScope{}, SessionReleaseBinding{}, ctx.Err()
	case admitted := <-result:
		if admitted.Err != nil {
			return AdmissionScope{}, SessionReleaseBinding{}, admitted.Err
		}
		entry := admitted.Val.(admissionCacheEntry)
		return entry.Scope, entry.Binding, nil
	}
}

func (c *AdmissionDecisionCache) InvalidateInstallation(installationID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	for _, key := range c.entries.Keys() {
		if key.Installation == installationID {
			c.entries.Remove(key)
		}
	}
}

// Release lifecycle changes can withdraw any retained binding in the fleet.
func (c *AdmissionDecisionCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	c.entries.Purge()
}
