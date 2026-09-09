package auth

import (
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// BlindExperimentCache caches active and inactive per-user experiment states.
type BlindExperimentCache interface {
	Get(routerUserID string) (BlindExperimentState, bool)
	Set(installationID, routerUserID string, state BlindExperimentState)
	SetError(installationID, routerUserID string)
	InvalidateInstallation(installationID string)
}

// NoOpBlindExperimentCache disables experiment caching.
type NoOpBlindExperimentCache struct{}

func (NoOpBlindExperimentCache) Get(string) (BlindExperimentState, bool) {
	return BlindExperimentState{}, false
}
func (NoOpBlindExperimentCache) Set(string, string, BlindExperimentState) {}
func (NoOpBlindExperimentCache) SetError(string, string)                  {}
func (NoOpBlindExperimentCache) InvalidateInstallation(string)            {}

// LRUBlindExperimentCache is keyed by router user and secondarily indexed by
// installation so Pub/Sub invalidation can evict a whole organization's cohort.
type LRUBlindExperimentCache struct {
	mu             sync.Mutex
	entries        *expirable.LRU[string, BlindExperimentState]
	now            Clock
	errorTTL       time.Duration
	byInstallation map[string]map[string]struct{}
	// A global epoch closes the Set/Invalidate race without retaining one
	// generation counter per installation forever. An invalidation may evict a
	// concurrent Set for another installation; that is safe because the next
	// request simply refills the cache.
	invalidationEpoch  uint64
	installationByUser map[string]string
}

// NewLRUBlindExperimentCache constructs the per-user experiment cache.
func NewLRUBlindExperimentCache(size int, ttl time.Duration, now Clock) *LRUBlindExperimentCache {
	errorTTL := ttl
	if errorTTL <= 0 || errorTTL > 10*time.Second {
		errorTTL = 10 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	cache := &LRUBlindExperimentCache{
		now:                now,
		byInstallation:     make(map[string]map[string]struct{}),
		installationByUser: make(map[string]string),
		errorTTL:           errorTTL,
	}
	cache.entries = expirable.NewLRU(size, cache.onEvict, ttl)
	return cache
}

func (cache *LRUBlindExperimentCache) Get(routerUserID string) (BlindExperimentState, bool) {
	state, ok := cache.entries.Get(routerUserID)
	if !ok {
		return BlindExperimentState{}, false
	}
	if !state.errorCachedUntil.IsZero() && !cache.now().Before(state.errorCachedUntil) {
		// Leave the expired entry in place until the next successful Set or
		// installation invalidation. Returning a miss without removing it avoids
		// racing a concurrent Set that may have already refreshed the entry.
		return BlindExperimentState{}, false
	}
	return state, true
}

func (cache *LRUBlindExperimentCache) Set(installationID, routerUserID string, state BlindExperimentState) {
	if routerUserID == "" {
		return
	}
	var epoch uint64
	cache.mu.Lock()
	if installationID != "" {
		epoch = cache.invalidationEpoch
		if previousInstallationID, ok := cache.installationByUser[routerUserID]; ok && previousInstallationID != installationID {
			cache.removeFromIndexLocked(previousInstallationID, routerUserID)
		}
		users, ok := cache.byInstallation[installationID]
		if !ok {
			users = make(map[string]struct{}, 1)
			cache.byInstallation[installationID] = users
		}
		users[routerUserID] = struct{}{}
		cache.installationByUser[routerUserID] = installationID
	}
	cache.mu.Unlock()
	cache.entries.Add(routerUserID, state)
	if installationID == "" {
		return
	}
	cache.mu.Lock()
	if cache.invalidationEpoch != epoch {
		cache.removeFromIndexLocked(installationID, routerUserID)
		cache.mu.Unlock()
		cache.entries.Remove(routerUserID)
		return
	}
	cache.mu.Unlock()
}

// SetError caches a failed assignment read only for the short retry window.
// Requests fail open while the window is active, then retry the repository so
// a recovered database restores the experiment without waiting for the normal
// assignment TTL or an invalidation event.
func (cache *LRUBlindExperimentCache) SetError(installationID, routerUserID string) {
	cache.Set(installationID, routerUserID, BlindExperimentState{
		errorCachedUntil: cache.now().Add(cache.errorTTL),
	})
}

// InvalidateInstallation evicts every experiment state for an installation.
func (cache *LRUBlindExperimentCache) InvalidateInstallation(installationID string) {
	if installationID == "" {
		return
	}
	cache.mu.Lock()
	users := cache.byInstallation[installationID]
	delete(cache.byInstallation, installationID)
	cache.invalidationEpoch++
	for routerUserID := range users {
		delete(cache.installationByUser, routerUserID)
	}
	cache.mu.Unlock()
	for routerUserID := range users {
		cache.entries.Remove(routerUserID)
	}
}

func (cache *LRUBlindExperimentCache) onEvict(routerUserID string, _ BlindExperimentState) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	installationID, ok := cache.installationByUser[routerUserID]
	if !ok {
		return
	}
	cache.removeFromIndexLocked(installationID, routerUserID)
}

func (cache *LRUBlindExperimentCache) removeFromIndexLocked(installationID, routerUserID string) {
	delete(cache.installationByUser, routerUserID)
	users, ok := cache.byInstallation[installationID]
	if !ok {
		return
	}
	delete(users, routerUserID)
	if len(users) == 0 {
		delete(cache.byInstallation, installationID)
	}
}

var (
	_ InstallationInvalidator = (*LRUBlindExperimentCache)(nil)
	_ BlindExperimentCache    = (*LRUBlindExperimentCache)(nil)
	_ BlindExperimentCache    = NoOpBlindExperimentCache{}
)
