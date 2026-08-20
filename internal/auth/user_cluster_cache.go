package auth

import (
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// InstallationInvalidator is the subset of a cache the Pub/Sub invalidation
// listener needs. Both LRUAPIKeyCache and LRUUserClusterListCache implement it,
// so one installation-changed message fans out to every cache holding
// installation-scoped state.
type InstallationInvalidator interface {
	InvalidateInstallation(installationID string)
}

// UserClusterListCache is an in-process read-through cache for per-user
// cluster selections. Keyed by router user, not API key: one key serves every
// user on an installation, so this state cannot live in the API-key cache.
type UserClusterListCache interface {
	Get(routerUserID string) ([]UserClusterModelList, bool)
	Set(installationID, routerUserID string, lists []UserClusterModelList)
	InvalidateInstallation(installationID string)
}

// NoOpUserClusterListCache is the Null Object: every Get misses.
type NoOpUserClusterListCache struct{}

func (NoOpUserClusterListCache) Get(string) ([]UserClusterModelList, bool) { return nil, false }
func (NoOpUserClusterListCache) Set(string, string, []UserClusterModelList) {
}
func (NoOpUserClusterListCache) InvalidateInstallation(string) {}

// LRUUserClusterListCache caches per-user cluster selections with a byInstallation
// secondary index so installation-invalidation Pub/Sub messages evict them too.
// Mirrors LRUAPIKeyCache (including the invalidationGen race guard). Caches a
// successful empty result: most users configure nothing, so an uncached miss would
// hit the DB on every request from every unconfigured user.
type LRUUserClusterListCache struct {
	mu             sync.Mutex
	entries        *expirable.LRU[string, []UserClusterModelList]
	byInstallation map[string]map[string]struct{}
	// invalidationGen detects invalidation races between index update and LRU
	// insert. We cannot hold mu across entries.Add because the eviction
	// callback acquires mu, which would deadlock on a capacity eviction.
	invalidationGen map[string]uint64
	// installationByUser lets the eviction callback find the index bucket, since
	// the cached value carries no installation ID.
	installationByUser map[string]string
}

// NewLRUUserClusterListCache constructs the per-user cluster selection cache.
func NewLRUUserClusterListCache(size int, ttl time.Duration) *LRUUserClusterListCache {
	c := &LRUUserClusterListCache{
		byInstallation:     make(map[string]map[string]struct{}),
		invalidationGen:    make(map[string]uint64),
		installationByUser: make(map[string]string),
	}
	c.entries = expirable.NewLRU(size, c.onEvict, ttl)
	return c
}

func (c *LRUUserClusterListCache) Get(routerUserID string) ([]UserClusterModelList, bool) {
	return c.entries.Get(routerUserID)
}

func (c *LRUUserClusterListCache) Set(installationID, routerUserID string, lists []UserClusterModelList) {
	if routerUserID == "" {
		return
	}
	var preGen uint64
	c.mu.Lock()
	if installationID != "" {
		preGen = c.invalidationGen[installationID]
		users, ok := c.byInstallation[installationID]
		if !ok {
			users = make(map[string]struct{}, 1)
			c.byInstallation[installationID] = users
		}
		users[routerUserID] = struct{}{}
		c.installationByUser[routerUserID] = installationID
	}
	c.mu.Unlock()
	c.entries.Add(routerUserID, lists)
	if installationID == "" {
		return
	}
	// Closes the race with InvalidateInstallation: if a concurrent invalidation
	// drained the bucket between the index update and Add, the entry we just
	// inserted is orphaned, so evict it and roll back the index.
	c.mu.Lock()
	if c.invalidationGen[installationID] != preGen {
		c.removeFromIndexLocked(installationID, routerUserID)
		c.mu.Unlock()
		c.entries.Remove(routerUserID)
		return
	}
	c.mu.Unlock()
}

// InvalidateInstallation drops every cached selection for installationID so the
// next request re-reads from Postgres.
func (c *LRUUserClusterListCache) InvalidateInstallation(installationID string) {
	if installationID == "" {
		return
	}
	c.mu.Lock()
	users := c.byInstallation[installationID]
	delete(c.byInstallation, installationID)
	c.invalidationGen[installationID]++
	for user := range users {
		delete(c.installationByUser, user)
	}
	c.mu.Unlock()
	for user := range users {
		c.entries.Remove(user)
	}
}

// onEvict keeps the index in sync on eviction or TTL expiry. Must not hold c.mu
// when calling Remove (would deadlock with Add's capacity eviction).
func (c *LRUUserClusterListCache) onEvict(routerUserID string, _ []UserClusterModelList) {
	c.mu.Lock()
	defer c.mu.Unlock()
	installationID, ok := c.installationByUser[routerUserID]
	if !ok {
		return
	}
	c.removeFromIndexLocked(installationID, routerUserID)
}

// removeFromIndexLocked drops one user from the installation index. Caller holds c.mu.
func (c *LRUUserClusterListCache) removeFromIndexLocked(installationID, routerUserID string) {
	delete(c.installationByUser, routerUserID)
	users, ok := c.byInstallation[installationID]
	if !ok {
		return
	}
	delete(users, routerUserID)
	if len(users) == 0 {
		delete(c.byInstallation, installationID)
	}
}

var (
	_ InstallationInvalidator = (*LRUAPIKeyCache)(nil)
	_ InstallationInvalidator = (*LRUUserClusterListCache)(nil)
	_ UserClusterListCache    = (*LRUUserClusterListCache)(nil)
	_ UserClusterListCache    = NoOpUserClusterListCache{}
)
