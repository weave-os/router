package auth

import (
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// BlindExperimentCache caches active and inactive per-user experiment states.
type BlindExperimentCache interface {
	Get(routerUserID string) (BlindExperimentState, bool)
	InvalidationGeneration() uint64
	Set(installationID, routerUserID string, state BlindExperimentState)
	SetError(installationID, routerUserID string)
	InvalidateInstallation(installationID string)
}

// NoOpBlindExperimentCache disables experiment caching.
type NoOpBlindExperimentCache struct{}

func (NoOpBlindExperimentCache) Get(string) (BlindExperimentState, bool) {
	return BlindExperimentState{}, false
}
func (NoOpBlindExperimentCache) InvalidationGeneration() uint64           { return 0 }
func (NoOpBlindExperimentCache) Set(string, string, BlindExperimentState) {}
func (NoOpBlindExperimentCache) SetError(string, string)                  {}
func (NoOpBlindExperimentCache) InvalidateInstallation(string)            {}

// LRUBlindExperimentCache is keyed by router user and secondarily indexed by
// installation so Pub/Sub invalidation can evict a whole organization's cohort.
type LRUBlindExperimentCache struct {
	mu                      sync.Mutex
	entries                 *expirable.LRU[string, BlindExperimentState]
	errorEntries            *expirable.LRU[string, time.Time]
	now                     Clock
	errorTTL                time.Duration
	byInstallation          map[string]map[string]struct{}
	errorByInstallation     map[string]map[string]struct{}
	installationByUser      map[string]string
	errorInstallationByUser map[string]string
	// A global epoch closes the Set/Invalidate race without retaining one
	// generation counter per installation forever. An invalidation may evict a
	// concurrent Set for another installation; that is safe because the next
	// request simply refills the cache.
	invalidationEpoch uint64
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
		now:                     now,
		byInstallation:          make(map[string]map[string]struct{}),
		errorByInstallation:     make(map[string]map[string]struct{}),
		installationByUser:      make(map[string]string),
		errorInstallationByUser: make(map[string]string),
		errorTTL:                errorTTL,
	}
	cache.entries = expirable.NewLRU(size, cache.onEvict, ttl)
	cache.errorEntries = expirable.NewLRU(size, cache.onErrorEvict, errorTTL)
	return cache
}

func (cache *LRUBlindExperimentCache) Get(routerUserID string) (BlindExperimentState, bool) {
	state, ok := cache.entries.Get(routerUserID)
	if ok {
		return state, true
	}
	if errorExpiry, errorCached := cache.errorEntries.Get(routerUserID); errorCached {
		if cache.now().Before(errorExpiry) {
			return BlindExperimentState{}, true
		}
		// The injected clock can expire an entry before the LRU's wall-clock
		// timer. A miss is enough to trigger a retry; the next Set or
		// invalidation removes the stale index safely.
	}
	return BlindExperimentState{}, false
}

// InvalidationGeneration returns a monotonic token that changes whenever an
// installation invalidation evicts cached experiment state.
func (cache *LRUBlindExperimentCache) InvalidationGeneration() uint64 {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.invalidationEpoch
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
	cache.clearError(routerUserID)
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

// SetError caches a failed assignment read in a separate short-lived LRU so
// outages cannot evict valid experiment assignments from the normal cache.
func (cache *LRUBlindExperimentCache) SetError(installationID, routerUserID string) {
	if routerUserID == "" {
		return
	}
	expiresAt := cache.now().Add(cache.errorTTL)
	var epoch uint64
	cache.mu.Lock()
	if installationID != "" {
		epoch = cache.invalidationEpoch
		if previousInstallationID, ok := cache.errorInstallationByUser[routerUserID]; ok && previousInstallationID != installationID {
			cache.removeErrorFromIndexLocked(previousInstallationID, routerUserID)
		}
		users, ok := cache.errorByInstallation[installationID]
		if !ok {
			users = make(map[string]struct{}, 1)
			cache.errorByInstallation[installationID] = users
		}
		users[routerUserID] = struct{}{}
		cache.errorInstallationByUser[routerUserID] = installationID
	}
	cache.mu.Unlock()
	cache.errorEntries.Add(routerUserID, expiresAt)
	if installationID == "" {
		return
	}
	cache.mu.Lock()
	if cache.invalidationEpoch != epoch {
		cache.removeErrorFromIndexLocked(installationID, routerUserID)
		cache.mu.Unlock()
		cache.errorEntries.Remove(routerUserID)
		return
	}
	cache.mu.Unlock()
}

// InvalidateInstallation evicts every experiment state for an installation.
func (cache *LRUBlindExperimentCache) InvalidateInstallation(installationID string) {
	if installationID == "" {
		return
	}
	cache.mu.Lock()
	users := cache.byInstallation[installationID]
	errorUsers := cache.errorByInstallation[installationID]
	delete(cache.byInstallation, installationID)
	delete(cache.errorByInstallation, installationID)
	cache.invalidationEpoch++
	for routerUserID := range users {
		delete(cache.installationByUser, routerUserID)
	}
	for routerUserID := range errorUsers {
		delete(cache.errorInstallationByUser, routerUserID)
	}
	cache.mu.Unlock()
	for routerUserID := range users {
		cache.entries.Remove(routerUserID)
	}
	for routerUserID := range errorUsers {
		cache.errorEntries.Remove(routerUserID)
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

func (cache *LRUBlindExperimentCache) onErrorEvict(routerUserID string, _ time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	installationID, ok := cache.errorInstallationByUser[routerUserID]
	if !ok {
		return
	}
	cache.removeErrorFromIndexLocked(installationID, routerUserID)
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

func (cache *LRUBlindExperimentCache) clearError(routerUserID string) {
	cache.mu.Lock()
	installationID, ok := cache.errorInstallationByUser[routerUserID]
	if ok {
		cache.removeErrorFromIndexLocked(installationID, routerUserID)
	}
	cache.mu.Unlock()
	if ok {
		cache.errorEntries.Remove(routerUserID)
	}
}

func (cache *LRUBlindExperimentCache) removeErrorFromIndexLocked(installationID, routerUserID string) {
	delete(cache.errorInstallationByUser, routerUserID)
	users, ok := cache.errorByInstallation[installationID]
	if !ok {
		return
	}
	delete(users, routerUserID)
	if len(users) == 0 {
		delete(cache.errorByInstallation, installationID)
	}
}

var (
	_ InstallationInvalidator = (*LRUBlindExperimentCache)(nil)
	_ BlindExperimentCache    = (*LRUBlindExperimentCache)(nil)
	_ BlindExperimentCache    = NoOpBlindExperimentCache{}
)
