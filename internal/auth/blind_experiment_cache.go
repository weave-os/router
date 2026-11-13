package auth

import (
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// BlindExperimentCache caches active and inactive per-user experiment states.
type BlindExperimentCache interface {
	Enabled() bool
	GetAtGeneration(installationID, routerUserID string, generation uint64) (BlindExperimentState, bool)
	InstallationGeneration(installationID string) uint64
	SetAtGeneration(installationID, routerUserID string, state BlindExperimentState, generation uint64) bool
	SetErrorAtGeneration(installationID, routerUserID string, generation uint64) bool
	InvalidateInstallation(installationID string)
}

// NoOpBlindExperimentCache disables experiment caching.
type NoOpBlindExperimentCache struct{}

func (NoOpBlindExperimentCache) Enabled() bool { return false }
func (NoOpBlindExperimentCache) GetAtGeneration(string, string, uint64) (BlindExperimentState, bool) {
	return BlindExperimentState{}, false
}
func (NoOpBlindExperimentCache) InstallationGeneration(string) uint64 { return 0 }
func (NoOpBlindExperimentCache) SetAtGeneration(string, string, BlindExperimentState, uint64) bool {
	return false
}
func (NoOpBlindExperimentCache) SetErrorAtGeneration(string, string, uint64) bool { return false }
func (NoOpBlindExperimentCache) InvalidateInstallation(string)                    {}

type blindExperimentCacheEntry struct {
	state          BlindExperimentState
	installationID string
	generation     uint64
}

type blindExperimentErrorEntry struct {
	expiresAt      time.Time
	installationID string
	generation     uint64
}

// LRUBlindExperimentCache is keyed by router user and secondarily indexed by
// installation so Pub/Sub invalidation can evict a whole organization's cohort.
type LRUBlindExperimentCache struct {
	mu                      sync.Mutex
	entries                 *expirable.LRU[string, blindExperimentCacheEntry]
	errorEntries            *expirable.LRU[string, blindExperimentErrorEntry]
	now                     Clock
	errorTTL                time.Duration
	byInstallation          map[string]map[string]struct{}
	errorByInstallation     map[string]map[string]struct{}
	installationByUser      map[string]string
	errorInstallationByUser map[string]string
	installationGenerations map[string]uint64
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
		installationGenerations: make(map[string]uint64),
		errorTTL:                errorTTL,
	}
	cache.entries = expirable.NewLRU(size, cache.onEvict, ttl)
	cache.errorEntries = expirable.NewLRU(size, cache.onErrorEvict, errorTTL)
	return cache
}

func (*LRUBlindExperimentCache) Enabled() bool { return true }

// GetAtGeneration reads the LRUs before the index mutex because expirable LRU
// eviction callbacks run while holding the LRU's mutex.
func (cache *LRUBlindExperimentCache) GetAtGeneration(installationID, routerUserID string, generation uint64) (BlindExperimentState, bool) {
	entry, found := cache.entries.Get(routerUserID)
	if found && entry.installationID == installationID && entry.generation == generation && cache.assignmentIsCurrent(installationID, routerUserID, generation) {
		return entry.state, true
	}
	errorEntry, errorCached := cache.errorEntries.Get(routerUserID)
	if errorCached && errorEntry.installationID == installationID && errorEntry.generation == generation && cache.errorIsCurrent(installationID, routerUserID, generation) {
		if cache.now().Before(errorEntry.expiresAt) {
			return BlindExperimentState{}, true
		}
		// The injected clock can expire an entry before the LRU's wall-clock
		// timer. A miss is enough to trigger a retry; the next Set or
		// invalidation removes the stale index safely.
	}
	return BlindExperimentState{}, false
}

// InstallationGeneration returns the invalidation generation for one installation.
func (cache *LRUBlindExperimentCache) InstallationGeneration(installationID string) uint64 {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.installationGenerations[installationID]
}

// SetAtGeneration publishes an assignment only if its installation has not
// been invalidated since the caller began loading it.
func (cache *LRUBlindExperimentCache) SetAtGeneration(installationID, routerUserID string, state BlindExperimentState, generation uint64) bool {
	if installationID == "" || routerUserID == "" {
		return false
	}
	cache.mu.Lock()
	if cache.installationGenerations[installationID] != generation {
		cache.mu.Unlock()
		return false
	}
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
	cache.mu.Unlock()
	cache.entries.Add(routerUserID, blindExperimentCacheEntry{
		state:          state,
		installationID: installationID,
		generation:     generation,
	})
	cache.clearError(routerUserID)
	if !cache.assignmentIsCurrent(installationID, routerUserID, generation) {
		cache.mu.Lock()
		if cache.installationByUser[routerUserID] == installationID {
			cache.removeFromIndexLocked(installationID, routerUserID)
		}
		cache.mu.Unlock()
		cache.removeAssignmentIfMatches(installationID, routerUserID, generation)
		return false
	}
	return true
}

// SetErrorAtGeneration caches a failed assignment read in a separate
// short-lived LRU so outages cannot evict valid assignments.
func (cache *LRUBlindExperimentCache) SetErrorAtGeneration(installationID, routerUserID string, generation uint64) bool {
	if installationID == "" || routerUserID == "" {
		return false
	}
	expiresAt := cache.now().Add(cache.errorTTL)
	cache.mu.Lock()
	if cache.installationGenerations[installationID] != generation {
		cache.mu.Unlock()
		return false
	}
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
	cache.mu.Unlock()
	cache.errorEntries.Add(routerUserID, blindExperimentErrorEntry{
		expiresAt:      expiresAt,
		installationID: installationID,
		generation:     generation,
	})
	if !cache.errorIsCurrent(installationID, routerUserID, generation) {
		cache.mu.Lock()
		if cache.errorInstallationByUser[routerUserID] == installationID {
			cache.removeErrorFromIndexLocked(installationID, routerUserID)
		}
		cache.mu.Unlock()
		cache.removeErrorIfMatches(installationID, routerUserID, generation)
		return false
	}
	return true
}

func (cache *LRUBlindExperimentCache) assignmentIsCurrent(installationID, routerUserID string, generation uint64) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.installationGenerations[installationID] == generation && cache.installationByUser[routerUserID] == installationID
}

func (cache *LRUBlindExperimentCache) errorIsCurrent(installationID, routerUserID string, generation uint64) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.installationGenerations[installationID] == generation && cache.errorInstallationByUser[routerUserID] == installationID
}

func (cache *LRUBlindExperimentCache) removeAssignmentIfMatches(installationID, routerUserID string, generation uint64) {
	entry, found := cache.entries.Peek(routerUserID)
	if found && entry.installationID == installationID && entry.generation == generation {
		cache.entries.Remove(routerUserID)
	}
}

func (cache *LRUBlindExperimentCache) removeErrorIfMatches(installationID, routerUserID string, generation uint64) {
	entry, found := cache.errorEntries.Peek(routerUserID)
	if found && entry.installationID == installationID && entry.generation == generation {
		cache.errorEntries.Remove(routerUserID)
	}
}

// InvalidateInstallation evicts every experiment state for an installation.
func (cache *LRUBlindExperimentCache) InvalidateInstallation(installationID string) {
	if installationID == "" {
		return
	}
	cache.mu.Lock()
	users := cache.byInstallation[installationID]
	errorUsers := cache.errorByInstallation[installationID]
	invalidatedGeneration := cache.installationGenerations[installationID]
	delete(cache.byInstallation, installationID)
	delete(cache.errorByInstallation, installationID)
	cache.installationGenerations[installationID] = invalidatedGeneration + 1
	for routerUserID := range users {
		delete(cache.installationByUser, routerUserID)
	}
	for routerUserID := range errorUsers {
		delete(cache.errorInstallationByUser, routerUserID)
	}
	cache.mu.Unlock()
	for routerUserID := range users {
		cache.removeAssignmentIfMatches(installationID, routerUserID, invalidatedGeneration)
	}
	for routerUserID := range errorUsers {
		cache.removeErrorIfMatches(installationID, routerUserID, invalidatedGeneration)
	}
}

func (cache *LRUBlindExperimentCache) onEvict(routerUserID string, entry blindExperimentCacheEntry) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.installationByUser[routerUserID] != entry.installationID || cache.installationGenerations[entry.installationID] != entry.generation {
		return
	}
	cache.removeFromIndexLocked(entry.installationID, routerUserID)
}

func (cache *LRUBlindExperimentCache) onErrorEvict(routerUserID string, entry blindExperimentErrorEntry) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.errorInstallationByUser[routerUserID] != entry.installationID || cache.installationGenerations[entry.installationID] != entry.generation {
		return
	}
	cache.removeErrorFromIndexLocked(entry.installationID, routerUserID)
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
